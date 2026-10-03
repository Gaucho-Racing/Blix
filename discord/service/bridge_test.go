package service

import (
	"context"
	"encoding/json"
	"fmt"
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

func (fakeIdentity) VerifyDiscordMention(_ context.Context, id string) (bool, error) {
	if id == "888" {
		return false, &client.Error{Code: "upstream_unavailable"}
	}
	return id == "123", nil
}

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
	runID := i.RunID
	if runID == "" {
		runID = "run_" + i.ExternalMessageID
	}
	return client.InputResult{RunID: runID, SessionID: "session_" + runID, TurnID: "turn_" + i.ExternalMessageID}, nil
}
func (r *fakeRuntime) Events(context.Context, string, uint64) ([]client.Event, error) {
	return r.events, nil
}

type fakeDiscord struct {
	sent            []string
	references      []string
	channels        []string
	allowedMentions [][]string
	fail            bool
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
func (d *fakeDiscord) Send(_ context.Context, id, channel, replyTo, content, link string, allowedUserIDs []string) (string, error) {
	d.sent = append(d.sent, id)
	d.references = append(d.references, replyTo)
	d.channels = append(d.channels, channel)
	d.allowedMentions = append(d.allowedMentions, allowedUserIDs)
	if d.fail {
		d.fail = false
		return "", &client.Error{Code: "discord_delivery_uncertain"}
	}
	return fmt.Sprintf("%d", 900+len(d.sent)), nil
}

func fixture(t *testing.T) (*Bridge, *fakeRuntime, *fakeDiscord) {
	t.Helper()
	port := os.Getenv("BLIX_TEST_DATABASE_PORT")
	if port == "" {
		t.Skip("BLIX_TEST_DATABASE_PORT is required for PostgreSQL integration tests")
	}
	c := config.Config{GuildID: "100", PublicURL: "https://blix.example", DatabaseHost: "127.0.0.1", DatabasePort: port, DatabaseUser: "postgres", DatabasePassword: "blix-test", DatabaseName: "blix_test", DatabaseSSLMode: "disable"}
	db, err := database.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
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
	for _, channel := range []string{"300", "200"} {
		t.Run(channel, func(t *testing.T) { testInboxDeduplicationContextAndOwner(t, channel) })
	}
}

func testInboxDeduplicationContextAndOwner(t *testing.T, channel string) {
	b, r, _ := fixture(t)
	ctx := context.Background()
	m := mention("500", channel, "600")
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
	if c["guild_name"] != "Gaucho Racing" || c["channel_name"] != "engineering" || c["sentinel_entity_id"] != "ent_user" || (channel == "200" && c["thread_id"] != "200") || (channel == "300" && c["thread_id"] != nil) {
		t.Fatalf("context=%+v", c)
	}
	followup := mention("501", channel, "600")
	followup.MessageReference = &discordgo.MessageReference{MessageID: "500", ChannelID: channel}
	if err := b.Receive(ctx, "400", followup); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if r.inputs[1].RunID != "run_500" {
		t.Fatal("followup created new run")
	}
	foreign := mention("502", channel, "601")
	foreign.MessageReference = &discordgo.MessageReference{MessageID: "500", ChannelID: channel}
	if err := b.Receive(ctx, "400", foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 {
		t.Fatal("another user executed as the owner")
	}
	if err := b.Receive(ctx, "400", mention("503", channel, "601")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 3 || r.inputs[2].RunID != "" {
		t.Fatal("independent mention inherited another run")
	}
	var bindings []model.Binding
	if err := b.DB.Find(&bindings).Error; err != nil || len(bindings) != 2 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	for _, binding := range bindings {
		if binding.ChannelID != channel {
			t.Fatal("response channel changed")
		}
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
	turnID := "turn_500"
	r.events = []client.Event{{Position: 1, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnID, Payload: json.RawMessage(`{"content":"Full detailed answer","channel_content":"Short answer"}`)}, {Position: 2, Type: "message.assistant_v1", SessionID: "session_child", Payload: json.RawMessage(`{"content":"Internal child answer"}`)}}
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
	if err := b.DB.First(&reply, "id = ?", "event/run_500/1").Error; err != nil {
		t.Fatal(err)
	}
	if reply.Content != "Short answer" || reply.ChannelID != "300" || reply.ReplyToMessageID != "500" {
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
	for i := range d.sent {
		if d.channels[i] != "300" || d.references[i] != "500" {
			t.Fatal("did not reply directly to the original message")
		}
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

func TestDeliveryOnlyAllowsVerifiedUserMentions(t *testing.T) {
	b, _, d := fixture(t)
	output := model.Outbox{ID: "mentions", ChannelID: "300", ReplyToMessageID: "500", Content: "Ask <@123> or <@!123>. <@999> <@888> @everyone <@&456>", State: "pending", NextAttemptAt: time.Now()}
	if err := b.DB.Create(&output).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.sent) != 1 || len(d.allowedMentions[0]) != 1 || d.allowedMentions[0][0] != "123" {
		t.Fatalf("allowed mentions=%v", d.allowedMentions)
	}
}

func TestReplyToBotContinuesRunAndTargetsEachTurn(t *testing.T) {
	b, r, _ := fixture(t)
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"501", "502"} {
		m := mention(id, "300", "600")
		m.MessageReference = &discordgo.MessageReference{MessageID: "901", ChannelID: "300"}
		if err := b.Receive(ctx, "400", m); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ProcessInbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.inputs) != 3 || r.inputs[1].RunID != "run_500" || r.inputs[2].RunID != "run_500" {
		t.Fatalf("reply chain lost its run: %+v", r.inputs)
	}
	var binding model.Binding
	if err := b.DB.First(&binding, "id = ?", "500").Error; err != nil {
		t.Fatal(err)
	}
	turnA, turnB := "turn_501", "turn_502"
	events := []client.Event{
		{Position: 1, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnB, Payload: json.RawMessage(`{"channel_content":"Second answer"}`)},
		{Position: 2, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnA, Payload: json.RawMessage(`{"channel_content":"First answer"}`)},
	}
	if err := b.SaveEvents(ctx, binding, events); err != nil {
		t.Fatal(err)
	}
	for i, expected := range []string{"502", "501"} {
		var output model.Outbox
		if err := b.DB.First(&output, "id = ?", fmt.Sprintf("event/run_500/%d", i+1)).Error; err != nil || output.ReplyToMessageID != expected {
			t.Fatalf("wrong reply target: output=%+v err=%v", output, err)
		}
	}
	cancel := mention("503", "300", "600")
	cancel.Content = "<@400> cancel"
	cancel.MessageReference = &discordgo.MessageReference{MessageID: "901", ChannelID: "300"}
	if err := b.Receive(ctx, "400", cancel); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 4 || r.inputs[3].Operation != "cancel" || r.inputs[3].RunID != "run_500" {
		t.Fatalf("cancel targeted wrong run: %+v", r.inputs)
	}
}

func TestReplyCannotAttachToAnotherChannel(t *testing.T) {
	b, r, _ := fixture(t)
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	m := mention("501", "200", "600")
	m.MessageReference = &discordgo.MessageReference{MessageID: "500", ChannelID: "200"}
	if err := b.Receive(ctx, "400", m); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 || r.inputs[1].RunID != "" {
		t.Fatalf("attached to another channel's run: %+v", r.inputs)
	}
}

func TestExistingBindingsSurviveSchemaUpdate(t *testing.T) {
	b, _, _ := fixture(t)
	if err := b.DB.Exec("DROP TABLE discord_run_binding").Error; err != nil {
		t.Fatal(err)
	}
	if err := b.DB.Exec("CREATE TABLE discord_run_binding (channel_id text PRIMARY KEY, run_id text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := b.DB.Exec("INSERT INTO discord_run_binding (channel_id, run_id) VALUES ('200', 'run_existing')").Error; err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), b.Config)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	var binding model.Binding
	if err := db.First(&binding, "id = ?", "200").Error; err != nil || binding.ChannelID != "200" || binding.RunID != "run_existing" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if err := db.Create(&model.Binding{ID: "500", ChannelID: "200", RunID: "run_new"}).Error; err != nil {
		t.Fatalf("cannot create independent run in existing channel: %v", err)
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
