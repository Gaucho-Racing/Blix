package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/client"
	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/database"
	"github.com/gaucho-racing/blix/discord/model"
	"go.uber.org/zap"
)

type fakeIdentity struct{}

func (fakeIdentity) Resolve(context.Context, string) (client.Identity, error) {
	var i client.Identity
	json.Unmarshal([]byte(`{"id":"ent_user","type":"USER","user":{"id":"usr_user","entity_id":"ent_user"}}`), &i)
	i.Token = "caller-token"
	return i, nil
}

type fakeRuntime struct {
	inputs []client.Input
	events []client.Event
	fail   bool
}

func (r *fakeRuntime) Authorize(context.Context, client.Identity) error { return nil }
func (r *fakeRuntime) Submit(_ context.Context, i client.Input, token string) (client.InputResult, error) {
	r.inputs = append(r.inputs, i)
	if token != "caller-token" {
		panic("wrong caller token")
	}
	if r.fail {
		r.fail = false
		return client.InputResult{}, &client.Error{Code: "upstream_unavailable"}
	}
	return client.InputResult{RunID: "run_test", SessionID: "session_test", TurnID: "turn_test"}, nil
}
func (r *fakeRuntime) Events(context.Context, string, uint64) ([]client.Event, error) {
	return r.events, nil
}

type fakeDiscord struct {
	sent []string
	fail bool
}

func (d *fakeDiscord) Channel(_ context.Context, id string) (*discordgo.Channel, error) {
	if id == "200" {
		return &discordgo.Channel{ID: id, GuildID: "100", ParentID: "300", Type: discordgo.ChannelTypeGuildPublicThread, Name: "Blix thread"}, nil
	}
	return &discordgo.Channel{ID: id, GuildID: "100", Type: discordgo.ChannelTypeGuildText, Name: "engineering", Topic: "Team engineering"}, nil
}
func (d *fakeDiscord) Guild(context.Context, string) (*discordgo.Guild, error) {
	return &discordgo.Guild{ID: "100", Name: "Gaucho Racing"}, nil
}
func (d *fakeDiscord) Thread(context.Context, string, string, string) (*discordgo.Channel, error) {
	return d.Channel(context.Background(), "200")
}
func (d *fakeDiscord) Send(_ context.Context, id, channel, content, link string) (string, error) {
	d.sent = append(d.sent, id)
	if d.fail {
		d.fail = false
		return "", &client.Error{Code: "discord_delivery_uncertain"}
	}
	return "999", nil
}

func fixture(t *testing.T) (*Bridge, *fakeRuntime, *fakeDiscord) {
	t.Helper()
	port := os.Getenv("BLIX_TEST_DATABASE_PORT")
	if port == "" {
		t.Skip("BLIX_TEST_DATABASE_PORT is required for PostgreSQL integration tests")
	}
	c := config.Config{GuildID: "100", ChannelIDs: []string{"300"}, PublicURL: "https://blix.example", DatabaseHost: "127.0.0.1", DatabasePort: port, DatabaseUser: "postgres", DatabasePassword: "blix-test", DatabaseName: "blix_test", DatabaseSSLMode: "disable"}
	db, err := database.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("TRUNCATE discord_inbox,discord_run_binding,discord_outbox").Error; err != nil {
		t.Fatal(err)
	}
	r, d := &fakeRuntime{}, &fakeDiscord{}
	return &Bridge{DB: db, Config: c, Identity: fakeIdentity{}, Runtime: r, Discord: d, Log: zap.NewNop().Sugar()}, r, d
}
func mention(id, channel, author string) *discordgo.Message {
	return &discordgo.Message{ID: id, GuildID: "100", ChannelID: channel, Content: "<@400> How does braking work?", Author: &discordgo.User{ID: author}, Mentions: []*discordgo.User{{ID: "400"}}}
}

func TestInboxDeduplicationContextAndOwner(t *testing.T) {
	b, r, _ := fixture(t)
	ctx := context.Background()
	m := mention("500", "300", "600")
	for i := 0; i < 2; i++ {
		if err := b.Receive(ctx, "400", m); err != nil {
			t.Fatal(err)
		}
	}
	if work, err := b.ProcessInbox(ctx); !work || err != nil {
		t.Fatalf("work=%v err=%v", work, err)
	}
	if work, err := b.ProcessInbox(ctx); work || err != nil {
		t.Fatalf("duplicate work=%v err=%v", work, err)
	}
	if len(r.inputs) != 1 || r.inputs[0].RunID != "" || r.inputs[0].Content != "How does braking work?" {
		t.Fatalf("inputs=%+v", r.inputs)
	}
	var c map[string]any
	json.Unmarshal(r.inputs[0].Context, &c)
	if c["guild_name"] != "Gaucho Racing" || c["channel_name"] != "engineering" || c["thread_id"] != "200" || c["sentinel_entity_id"] != "ent_user" {
		t.Fatalf("context=%+v", c)
	}
	if err := b.Receive(ctx, "400", mention("501", "200", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if r.inputs[1].RunID != "run_test" {
		t.Fatal("followup created new run")
	}
	if err := b.Receive(ctx, "400", mention("502", "200", "601")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 {
		t.Fatal("another user executed as the owner")
	}
}

func TestRetryKeepsExactInputAndEventDelivery(t *testing.T) {
	b, r, d := fixture(t)
	ctx := context.Background()
	r.fail = true
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err == nil {
		t.Fatal("expected transient failure")
	}
	if err := b.DB.Model(&model.Inbox{}).Where("id = ?", "500").Update("next_attempt_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r.inputs[0])
	z, _ := json.Marshal(r.inputs[1])
	if string(a) != string(z) {
		t.Fatal("retry changed the idempotent payload")
	}
	r.events = []client.Event{{Position: 1, Type: "message.assistant_v1", SessionID: "session_test", Payload: json.RawMessage(`{"content":"Full detailed answer","channel_content":"Short answer"}`)}, {Position: 2, Type: "message.assistant_v1", SessionID: "session_child", Payload: json.RawMessage(`{"content":"Internal child answer"}`)}}
	for i := 0; i < 2; i++ {
		if err := b.PollEvents(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var outputs []model.Outbox
	if err := b.DB.Find(&outputs).Error; err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs=%+v", outputs)
	}
	var reply model.Outbox
	if err := b.DB.First(&reply, "id = ?", "event/run_test/1").Error; err != nil {
		t.Fatal(err)
	}
	if reply.Content != "Short answer" {
		t.Fatal("sent the full answer to Discord")
	}
	for i := 0; i < 2; i++ {
		if _, err := b.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if work, err := b.Deliver(ctx); work || err != nil {
		t.Fatalf("redelivered completed output: work=%v err=%v", work, err)
	}
	if len(d.sent) != 2 {
		t.Fatal("duplicate deliveries")
	}
}

func TestUncertainOldDeliveryDoesNotResend(t *testing.T) {
	b, _, d := fixture(t)
	past := time.Now().Add(-3 * time.Minute)
	output := model.Outbox{ID: "old", ChannelID: "200", Content: "Answer", State: "sending", FirstAttemptAt: &past, NextAttemptAt: past}
	if err := b.DB.Create(&output).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.Deliver(context.Background()); err == nil {
		t.Fatal("old delivery should require reconciliation")
	}
	if len(d.sent) != 0 {
		t.Fatal("resent an uncertain old output")
	}
	if err := b.DB.First(&output, "id = ?", "old").Error; err != nil || output.State != "uncertain" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}

func TestForeignAndBotMessagesAreIgnored(t *testing.T) {
	b, _, _ := fixture(t)
	ctx := context.Background()
	for _, edit := range []func(*discordgo.Message){func(m *discordgo.Message) { m.GuildID = "999" }, func(m *discordgo.Message) { m.Author.Bot = true }, func(m *discordgo.Message) { m.WebhookID = "123" }, func(m *discordgo.Message) { m.Mentions = nil }} {
		m := mention("500", "300", "600")
		edit(m)
		if err := b.Receive(ctx, "400", m); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := b.DB.Model(&model.Inbox{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("inbox=%d err=%v", count, err)
	}
}
