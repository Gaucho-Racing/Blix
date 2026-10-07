package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/model"
	"go.uber.org/zap"
)

func historyMessage(id, channel, content string) *discordgo.Message {
	return &discordgo.Message{ID: id, ChannelID: channel, Content: content, Author: &discordgo.User{ID: "600", Username: "Alex"}}
}

func TestMessageContextOrdersDeduplicatesAndIncludesOlderReply(t *testing.T) {
	d := &fakeDiscord{
		history: []*discordgo.Message{
			historyMessage("499", "200", "It may take an hour"),
			historyMessage("498", "200", "Use !verify"),
			historyMessage("501", "200", "Future message"),
			historyMessage("497", "300", "Other channel"),
			historyMessage("499", "200", "Duplicate"),
		},
		reply: historyMessage("450", "200", "Older claim"),
	}
	b := &Bridge{Discord: d, Log: zap.NewNop().Sugar()}
	transport := map[string]any{"thread_id": "200"}
	b.addMessageContext(context.Background(), &model.Inbox{ID: "500", GuildID: "100", ChannelID: "200", ReplyToMessageID: "450"}, transport)
	messages := transport["recent_messages"].([]contextMessage)
	if len(messages) != 3 || messages[0].ID != "450" || messages[1].ID != "498" || messages[2].ID != "499" {
		t.Fatalf("messages=%+v", messages)
	}
	if messages[2].URL != "https://discord.com/channels/100/200/499" || transport["reply_message_status"] != "available" {
		t.Fatalf("context=%+v", transport)
	}
}

func TestMessageContextFailurePreservesExplicitReply(t *testing.T) {
	d := &fakeDiscord{historyError: errors.New("permission denied"), reply: historyMessage("450", "200", "Claim")}
	b := &Bridge{Discord: d, Log: zap.NewNop().Sugar()}
	transport := map[string]any{}
	b.addMessageContext(context.Background(), &model.Inbox{ID: "500", GuildID: "100", ChannelID: "200", ReplyToMessageID: "450"}, transport)
	if transport["recent_messages_status"] != "unavailable" || len(transport["recent_messages"].([]contextMessage)) != 1 {
		t.Fatalf("context=%+v", transport)
	}
}

func TestMessageContextBoundsSerializedDataAndUTF8(t *testing.T) {
	d := &fakeDiscord{}
	for _, id := range []string{"499", "498", "497", "496", "495", "494", "493", "492"} {
		d.history = append(d.history, historyMessage(id, "200", strings.Repeat("\x00界", 1000)))
	}
	b := &Bridge{Discord: d, Log: zap.NewNop().Sugar()}
	transport := map[string]any{}
	b.addMessageContext(context.Background(), &model.Inbox{ID: "500", GuildID: "100", ChannelID: "200"}, transport)
	encoded, err := json.Marshal(transport)
	if err != nil || len(encoded) >= 16000 || transport["recent_messages_truncated"] != true {
		t.Fatalf("size=%d err=%v", len(encoded), err)
	}
	for _, message := range transport["recent_messages"].([]contextMessage) {
		if !message.Truncated || !utf8.ValidString(message.Content) {
			t.Fatalf("message=%+v", message)
		}
	}
}
