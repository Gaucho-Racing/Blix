package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/model"
)

const recentMessageLimit = 8

type contextMessage struct {
	ID         string    `json:"message_id"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Bot        bool      `json:"bot"`
	Timestamp  time.Time `json:"timestamp"`
	Content    string    `json:"content"`
	Truncated  bool      `json:"truncated"`
	ReplyTo    string    `json:"reply_to_message_id,omitempty"`
	URL        string    `json:"url"`
}

func boundedText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

func (b *Bridge) addMessageContext(ctx context.Context, input *model.Inbox, transport map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	messages, err := b.Discord.Messages(ctx, input.ChannelID, input.ID, recentMessageLimit)
	transport["recent_messages_status"] = "available"
	if err != nil {
		transport["recent_messages_status"] = "unavailable"
		b.Log.Warnw("Discord context history unavailable", "channel_id", input.ChannelID, "message_id", input.ID)
	}
	candidates := make([]*discordgo.Message, 0, recentMessageLimit+1)
	if input.ReplyToMessageID != "" {
		var reply *discordgo.Message
		for _, message := range messages {
			if message != nil && message.ID == input.ReplyToMessageID {
				reply = message
				break
			}
		}
		if reply == nil {
			reply, err = b.Discord.Message(ctx, input.ChannelID, input.ReplyToMessageID)
		}
		if reply != nil && err == nil {
			candidates = append(candidates, reply)
			transport["reply_message_status"] = "available"
		} else {
			transport["reply_message_status"] = "unavailable"
		}
	}
	if len(messages) > recentMessageLimit {
		messages = messages[:recentMessageLimit]
	}
	candidates = append(candidates, messages...)
	recent := make([]contextMessage, 0, recentMessageLimit+1)
	transport["recent_messages"] = recent
	seen := map[string]bool{}
	currentID, _ := strconv.ParseUint(input.ID, 10, 64)
	for _, message := range candidates {
		if message == nil || message.Author == nil || message.ChannelID != input.ChannelID ||
			!config.Snowflake(message.ID) || seen[message.ID] {
			continue
		}
		id, _ := strconv.ParseUint(message.ID, 10, 64)
		if id >= currentID {
			continue
		}
		seen[message.ID] = true
		content, truncated := boundedText(message.Content, 1200)
		name, _ := boundedText(message.Author.GlobalName, 100)
		if name == "" {
			name, _ = boundedText(message.Author.Username, 100)
		}
		item := contextMessage{
			ID: message.ID, AuthorID: message.Author.ID, AuthorName: name, Bot: message.Author.Bot,
			Timestamp: message.Timestamp, Content: content, Truncated: truncated,
			URL: fmt.Sprintf("https://discord.com/channels/%s/%s/%s", input.GuildID, input.ChannelID, message.ID),
		}
		if message.MessageReference != nil {
			item.ReplyTo = message.MessageReference.MessageID
		}
		candidate := append(append([]contextMessage{}, recent...), item)
		transport["recent_messages"] = candidate
		encoded, marshalErr := json.Marshal(transport)
		if marshalErr != nil || len(encoded) > 14000 {
			transport["recent_messages_truncated"] = true
			transport["recent_messages"] = recent
			continue
		}
		recent = candidate
		if content == "" {
			transport["recent_messages_content_may_be_unavailable"] = true
		}
	}
	sort.Slice(recent, func(i, j int) bool {
		left, _ := strconv.ParseUint(recent[i].ID, 10, 64)
		right, _ := strconv.ParseUint(recent[j].ID, 10, 64)
		return left < right
	})
	transport["recent_messages"] = recent
}
