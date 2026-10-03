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
	sources         []string
	reacted         []string
	removed         []string
	removeError     error
	reactionError   error
	deleted         []string
	operations      []string
	deleteError     error
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
func (d *fakeDiscord) React(_ context.Context, channel, message, emoji string) error {
	d.reacted = append(d.reacted, channel+"/"+message+"/"+emoji)
	return d.reactionError
}
func (d *fakeDiscord) RemoveReaction(_ context.Context, channel, message, emoji string) error {
	d.removed = append(d.removed, channel+"/"+message+"/"+emoji)
	return d.removeError
}
func (d *fakeDiscord) Delete(_ context.Context, channel, message string) error {
	d.operations = append(d.operations, "delete/"+channel+"/"+message)
	d.deleted = append(d.deleted, channel+"/"+message)
	return d.deleteError
}
func (d *fakeDiscord) Send(_ context.Context, id, channel, replyTo, content, link, sourcesURL string, allowedUserIDs []string) (string, error) {
	d.operations = append(d.operations, "send/"+id)
	d.sent = append(d.sent, id)
	d.sources = append(d.sources, sourcesURL)
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
	if len(outputs) != 4 {
		t.Fatalf("outputs=%+v", outputs)
	}
	var reply model.Outbox
	if err := b.DB.First(&reply, "id = ?", "event/run_500/1").Error; err != nil {
		t.Fatal(err)
	}
	if reply.Content != "Short answer" || reply.ChannelID != "300" || reply.ReplyToMessageID != "500" {
		t.Fatal("sent the full answer to Discord")
	}
	for i := 0; i < 4; i++ {
		if _, err := b.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if work, err := b.Deliver(ctx); work || err != nil {
		t.Fatalf("redelivered completed output: work=%v err=%v", work, err)
	}
	if len(d.sent) != 1 || len(d.reacted) != 1 || len(d.removed) != 1 || len(d.deleted) != 0 {
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

func TestOldReactionDeliveryCanRetryWithoutSendingMessage(t *testing.T) {
	b, _, d := fixture(t)
	past := time.Now().Add(-3 * time.Minute)
	output := model.Outbox{ID: "eyes", ChannelID: "300", ReplyToMessageID: "500", Reaction: "👀", State: "sending", FirstAttemptAt: &past, NextAttemptAt: past}
	if err := b.DB.Create(&output).Error; err != nil {
		t.Fatal(err)
	}
	if work, err := b.Deliver(context.Background()); !work || err != nil {
		t.Fatalf("reaction delivery work=%v err=%v", work, err)
	}
	if len(d.sent) != 0 || len(d.reacted) != 1 || d.reacted[0] != "300/500/👀" {
		t.Fatalf("sent=%v reacted=%v", d.sent, d.reacted)
	}
	if err := b.DB.First(&output, "id = ?", "eyes").Error; err != nil || output.State != "sent" || output.DiscordMessageID != "" {
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
	initialTurn := "turn_500"
	r.events = []client.Event{{Position: 1, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &initialTurn, Payload: json.RawMessage(`{"channel_content":"Initial answer"}`)}}
	if err := b.PollEvents(ctx); err != nil {
		t.Fatal(err)
	}
	drainReady(t, b)
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
		{Position: 2, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnB, Payload: json.RawMessage(`{"channel_content":"Second answer"}`)},
		{Position: 3, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnA, Payload: json.RawMessage(`{"channel_content":"First answer"}`)},
	}
	if err := b.SaveEvents(ctx, binding, events); err != nil {
		t.Fatal(err)
	}
	for i, expected := range []string{"502", "501"} {
		var output model.Outbox
		if err := b.DB.First(&output, "id = ?", fmt.Sprintf("event/run_500/%d", i+2)).Error; err != nil || output.ReplyToMessageID != expected {
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

func startWithPlaceholder(t *testing.T, b *Bridge) {
	t.Helper()
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if work, err := b.ProcessInbox(ctx); !work || err != nil {
		t.Fatalf("start work=%v error=%v", work, err)
	}
	var output model.Outbox
	if err := b.DB.First(&output, "id = ?", "input/500").Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, reply := range workingReplies {
		found = found || output.Content == reply
	}
	if !output.Placeholder || output.Reaction != "" || !found || output.NextAttemptAt.Sub(output.CreatedAt) < placeholderDelay-time.Second {
		t.Fatalf("invalid placeholder: %+v", output)
	}
	if work, err := b.Deliver(ctx); !work || err != nil {
		t.Fatalf("placeholder work=%v error=%v", work, err)
	}
	if work, err := b.Deliver(ctx); work || err != nil {
		t.Fatalf("placeholder delivered too early: work=%v error=%v", work, err)
	}
	makePlaceholderDue(t, b, "500")
	drainReady(t, b)
}

func completeTurn(t *testing.T, b *Bridge, r *fakeRuntime, eventType string) {
	t.Helper()
	turnID := "turn_500"
	r.events = []client.Event{{Position: 1, Type: eventType, SessionID: "session_run_500", TurnID: &turnID, Payload: json.RawMessage(`{"channel_content":"Final answer"}`)}}
	for i := 0; i < 2; i++ {
		if err := b.PollEvents(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceFooterUsesAssistantEventMetadata(t *testing.T) {
	for _, hasSources := range []bool{false, true} {
		t.Run(fmt.Sprint(hasSources), func(t *testing.T) {
			b, r, d := fixture(t)
			startWithPlaceholder(t, b)
			turnID := "turn_500"
			payload, err := json.Marshal(map[string]any{"message_id": "msg_123", "content": "Full cited answer", "channel_content": "Short answer with a requested link https://example.com", "channel_has_sources": hasSources})
			if err != nil {
				t.Fatal(err)
			}
			r.events = []client.Event{{Position: 1, Type: "message.assistant_v1", SessionID: "session_run_500", TurnID: &turnID, Payload: payload}}
			for i := 0; i < 2; i++ {
				if err := b.PollEvents(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			drainReady(t, b)
			expected := ""
			if hasSources {
				expected = "https://blix.example/chat/run_500#message-msg_123"
			}
			if len(d.sources) != 2 || d.sources[0] != "" || d.sources[1] != expected {
				t.Fatalf("incorrect sources links: %v", d.sources)
			}
		})
	}
}

func TestResponseDeletesPlaceholderBeforeReplying(t *testing.T) {
	for _, eventType := range []string{"message.assistant_v1", "attempt.failed_v1", "approval.requested_v1"} {
		t.Run(eventType, func(t *testing.T) {
			b, r, d := fixture(t)
			startWithPlaceholder(t, b)
			completeTurn(t, b, r, eventType)
			for i := 0; i < 2; i++ {
				if work, err := b.Deliver(context.Background()); !work || err != nil {
					t.Fatalf("delivery work=%v error=%v", work, err)
				}
			}
			expected := []string{"send/input/500", "delete/300/901", "send/event/run_500/1"}
			if fmt.Sprint(d.operations) != fmt.Sprint(expected) || d.references[1] != "500" || len(d.reacted) != 1 || len(d.removed) != 1 {
				t.Fatalf("wrong lifecycle: operations=%v references=%v", d.operations, d.references)
			}
			var placeholder model.Outbox
			if err := b.DB.First(&placeholder, "id = ?", "input/500").Error; err != nil || placeholder.State != "deleted" {
				t.Fatalf("placeholder=%+v error=%v", placeholder, err)
			}
			if work, err := b.Deliver(context.Background()); work || err != nil {
				t.Fatalf("duplicate work=%v error=%v", work, err)
			}
		})
	}
}

func TestCleanupRetriesAcrossRestartWithoutDuplicateReplies(t *testing.T) {
	b, r, d := fixture(t)
	startWithPlaceholder(t, b)
	completeTurn(t, b, r, "message.assistant_v1")
	d.deleteError = &client.Error{Status: 503, Code: "discord_cleanup_failed"}
	if work, err := b.Deliver(context.Background()); !work || err == nil {
		t.Fatalf("cleanup work=%v error=%v", work, err)
	}
	if work, err := b.Deliver(context.Background()); work || err != nil {
		t.Fatalf("answer bypassed cleanup: work=%v error=%v", work, err)
	}
	past := time.Now().Add(-3 * time.Minute)
	if err := b.DB.Model(&model.Outbox{}).Where("id = ?", "event/run_500/1/cleanup").Updates(map[string]any{"first_attempt_at": past, "next_attempt_at": past}).Error; err != nil {
		t.Fatal(err)
	}
	restarted := *b
	d.deleteError = nil
	for i := 0; i < 2; i++ {
		if work, err := restarted.Deliver(context.Background()); !work || err != nil {
			t.Fatalf("retry work=%v error=%v", work, err)
		}
	}
	if len(d.sent) != 2 || len(d.deleted) != 2 {
		t.Fatalf("sent=%v deleted=%v", d.sent, d.deleted)
	}
	if work, err := restarted.Deliver(context.Background()); work || err != nil {
		t.Fatalf("redelivered completed response: work=%v error=%v", work, err)
	}
}

func TestPermanentCleanupFailureDoesNotSuppressAnswer(t *testing.T) {
	b, r, d := fixture(t)
	startWithPlaceholder(t, b)
	completeTurn(t, b, r, "message.assistant_v1")
	d.deleteError = &client.Error{Status: 403, Code: "discord_cleanup_failed"}
	if _, err := b.Deliver(context.Background()); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if work, err := b.Deliver(context.Background()); !work || err != nil {
		t.Fatalf("answer work=%v error=%v", work, err)
	}
	if len(d.sent) != 2 {
		t.Fatalf("missing answer: %v", d.sent)
	}
	var cleanup model.Outbox
	if err := b.DB.First(&cleanup, "id = ?", "event/run_500/1/cleanup").Error; err != nil || cleanup.State != "failed" || cleanup.ErrorCode != "discord_cleanup_failed" {
		t.Fatalf("cleanup=%+v error=%v", cleanup, err)
	}
}

func TestAnswerRetryDoesNotRepeatPlaceholderDeletion(t *testing.T) {
	b, r, d := fixture(t)
	startWithPlaceholder(t, b)
	completeTurn(t, b, r, "message.assistant_v1")
	if _, err := b.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.fail = true
	if _, err := b.Deliver(context.Background()); err == nil {
		t.Fatal("expected send failure")
	}
	if err := b.DB.Model(&model.Outbox{}).Where("id = ?", "event/run_500/1").Update("next_attempt_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.deleted) != 1 || len(d.sent) != 3 || d.sent[1] != d.sent[2] {
		t.Fatalf("sent=%v deleted=%v", d.sent, d.deleted)
	}
}

func TestResponseOnlyRemovesItsOwnTurnsPlaceholder(t *testing.T) {
	b, r, d := fixture(t)
	startWithPlaceholder(t, b)
	m := mention("501", "300", "600")
	m.MessageReference = &discordgo.MessageReference{MessageID: "901", ChannelID: "300"}
	if err := b.Receive(context.Background(), "400", m); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	makePlaceholderDue(t, b, "501")
	drainReady(t, b)
	completeTurn(t, b, r, "message.assistant_v1")
	for i := 0; i < 2; i++ {
		if _, err := b.Deliver(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.deleted) != 1 || d.deleted[0] != "300/901" {
		t.Fatalf("deleted another turn's placeholder: %v", d.deleted)
	}
	var second model.Outbox
	if err := b.DB.First(&second, "id = ?", "input/501").Error; err != nil || second.State != "sent" {
		t.Fatalf("second turn=%+v error=%v", second, err)
	}
}

func TestCancelRemovesOutstandingPlaceholders(t *testing.T) {
	b, r, d := fixture(t)
	startWithPlaceholder(t, b)
	m := mention("501", "300", "600")
	m.Content = "<@400> cancel"
	m.MessageReference = &discordgo.MessageReference{MessageID: "901", ChannelID: "300"}
	if err := b.Receive(context.Background(), "400", m); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := b.Deliver(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.inputs) != 2 || r.inputs[1].Operation != "cancel" || len(d.deleted) != 1 || d.deleted[0] != "300/901" || len(d.sent) != 2 {
		t.Fatalf("inputs=%v operations=%v", r.inputs, d.operations)
	}
}

func makePlaceholderDue(t *testing.T, b *Bridge, inputID string) {
	t.Helper()
	if err := b.DB.Model(&model.Outbox{}).Where("id = ?", "input/"+inputID).Update("next_attempt_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
}
func drainReady(t *testing.T, b *Bridge) {
	t.Helper()
	for i := 0; i < 20; i++ {
		work, err := b.Deliver(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !work {
			return
		}
	}
	t.Fatal("delivery queue did not drain")
}

func TestFastReplySkipsDelayedPlaceholderAndRemovesEyesAfterAnswer(t *testing.T) {
	b, r, d := fixture(t)
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
	if len(d.reacted) != 1 || len(d.sent) != 0 {
		t.Fatalf("initial feedback: reacted=%v sent=%v", d.reacted, d.sent)
	}
	if work, err := b.Deliver(ctx); work || err != nil {
		t.Fatalf("early placeholder: work=%v error=%v", work, err)
	}
	completeTurn(t, b, r, "message.assistant_v1")
	for i := 0; i < 2; i++ {
		if _, err := b.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.sent) != 1 || d.sent[0] != "event/run_500/1" || len(d.removed) != 0 {
		t.Fatalf("response feedback: sent=%v removed=%v", d.sent, d.removed)
	}
	drainReady(t, b)
	if len(d.removed) != 1 || d.removed[0] != "300/500/👀" {
		t.Fatalf("missing eyes cleanup: %v", d.removed)
	}
	makePlaceholderDue(t, b, "500")
	drainReady(t, b)
	if len(d.sent) != 1 {
		t.Fatalf("late placeholder: %v", d.sent)
	}
}

func TestDelayedPlaceholderSurvivesRestartAndRemovesEyes(t *testing.T) {
	b, _, d := fixture(t)
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	drainReady(t, b)
	if len(d.sent) != 0 || len(d.reacted) != 1 || len(d.removed) != 0 {
		t.Fatalf("premature placeholder: sent=%v reacted=%v removed=%v", d.sent, d.reacted, d.removed)
	}
	restarted := *b
	makePlaceholderDue(t, &restarted, "500")
	if work, err := restarted.Deliver(ctx); !work || err != nil {
		t.Fatalf("placeholder: work=%v error=%v", work, err)
	}
	if len(d.sent) != 1 || len(d.removed) != 0 {
		t.Fatalf("placeholder transition: sent=%v removed=%v", d.sent, d.removed)
	}
	drainReady(t, &restarted)
	if len(d.removed) != 1 {
		t.Fatalf("missing removal: %v", d.removed)
	}
}

func TestReactionRemovalRetryDoesNotResendAnswer(t *testing.T) {
	b, r, d := fixture(t)
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
	completeTurn(t, b, r, "message.assistant_v1")
	for i := 0; i < 2; i++ {
		if _, err := b.Deliver(ctx); err != nil {
			t.Fatal(err)
		}
	}
	d.removeError = &client.Error{Status: 503, Code: "discord_reaction_cleanup_failed"}
	if _, err := b.Deliver(ctx); err == nil {
		t.Fatal("expected transient removal failure")
	}
	past := time.Now().Add(-3 * time.Minute)
	if err := b.DB.Model(&model.Outbox{}).Where("id = ?", "eyes/remove/500").Updates(map[string]any{"first_attempt_at": past, "next_attempt_at": past}).Error; err != nil {
		t.Fatal(err)
	}
	d.removeError = nil
	restarted := *b
	drainReady(t, &restarted)
	if len(d.sent) != 1 || len(d.removed) != 2 {
		t.Fatalf("cleanup resent answer: sent=%v removed=%v", d.sent, d.removed)
	}
}

func TestFailedReactionDoesNotReappearAfterFastAnswer(t *testing.T) {
	b, r, d := fixture(t)
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	d.reactionError = &client.Error{Status: 503, Code: "discord_reaction_failed"}
	if _, err := b.Deliver(ctx); err == nil {
		t.Fatal("expected reaction failure")
	}
	completeTurn(t, b, r, "message.assistant_v1")
	drainReady(t, b)
	var eyes model.Outbox
	if err := b.DB.First(&eyes, "id = ?", "eyes/add/500").Error; err != nil || eyes.State != "cancelled" {
		t.Fatalf("eyes=%+v error=%v", eyes, err)
	}
	if len(d.sent) != 1 || len(d.removed) != 1 {
		t.Fatalf("sent=%v removed=%v", d.sent, d.removed)
	}
	if err := b.DB.Model(&eyes).Update("next_attempt_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	d.reactionError = nil
	drainReady(t, b)
	if len(d.reacted) != 1 {
		t.Fatalf("reaction reappeared after answer: %v", d.reacted)
	}
}

func TestPlaceholderDelayIncludesTimeSpentStartingRun(t *testing.T) {
	b, _, d := fixture(t)
	ctx := context.Background()
	if err := b.Receive(ctx, "400", mention("500", "300", "600")); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.Model(&model.Inbox{}).Where("id = ?", "500").Update("created_at", time.Now().Add(-7*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	drainReady(t, b)
	if len(d.sent) != 1 || d.sent[0] != "input/500" || len(d.removed) != 1 {
		t.Fatalf("delay restarted after setup: sent=%v removed=%v", d.sent, d.removed)
	}
}
