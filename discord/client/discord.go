package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf16"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/config"
)

type Discord struct {
	Session   *discordgo.Session
	Connected atomic.Bool
	BotID     atomic.Value
}

func NewDiscord(token string) (*Discord, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.New("initialize Discord session")
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages
	s.MaxRestRetries = 0
	d := &Discord{Session: s}
	d.BotID.Store("")
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) { d.BotID.Store(r.User.ID); d.Connected.Store(true) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { d.Connected.Store(true) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { d.Connected.Store(false) })
	return d, nil
}
func (d *Discord) Channel(ctx context.Context, id string) (*discordgo.Channel, error) {
	return d.Session.Channel(id, discordgo.WithContext(ctx))
}
func (d *Discord) Guild(ctx context.Context, id string) (*discordgo.Guild, error) {
	if g, err := d.Session.State.Guild(id); err == nil {
		return g, nil
	}
	return d.Session.Guild(id, discordgo.WithContext(ctx))
}
func (d *Discord) React(ctx context.Context, channelID, messageID, emoji string) error {
	err := d.Session.MessageReactionAdd(channelID, messageID, emoji, discordgo.WithContext(ctx))
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) {
			return &Error{Status: rest.Response.StatusCode, Code: "discord_reaction_failed"}
		}
		return &Error{Code: "discord_reaction_uncertain"}
	}
	return nil
}
func (d *Discord) Delete(ctx context.Context, channelID, messageID string) error {
	err := d.Session.ChannelMessageDelete(channelID, messageID, discordgo.WithContext(ctx))
	if err == nil {
		return nil
	}
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		if rest.Response.StatusCode == http.StatusNotFound {
			return nil
		}
		return &Error{Status: rest.Response.StatusCode, Code: "discord_cleanup_failed"}
	}
	return &Error{Code: "discord_cleanup_uncertain"}
}
func Nonce(id string) string { sum := sha256.Sum256([]byte(id)); return hex.EncodeToString(sum[:12]) }
func Preview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "I’ve finished. Open Blix for the full answer."
	}
	return truncate(text, 1800)
}
func truncate(text string, limit int) string {
	var b strings.Builder
	units := 0
	for _, r := range text {
		n := utf16.RuneLen(r)
		if n < 1 {
			n = 1
		}
		if units+n > limit {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		units += n
	}
	return b.String()
}

var userMentionPattern = regexp.MustCompile(`<@!?([1-9][0-9]{0,19})>`)

func UserMentionIDs(content string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, match := range userMentionPattern.FindAllStringSubmatch(content, -1) {
		id := match[1]
		if !seen[id] && config.Snowflake(id) && len(ids) < 100 {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func (d *Discord) Send(ctx context.Context, id, channelID, replyToMessageID, content, runURL string, allowedUserIDs []string) (string, error) {
	text := Preview(content)
	message := discordgo.MessageSend{
		Components:      []discordgo.MessageComponent{discordgo.TextDisplay{Content: text}},
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
		Flags:           discordgo.MessageFlagsIsComponentsV2 | discordgo.MessageFlagsSuppressEmbeds,
	}
	allowed := map[string]bool{}
	for _, id := range allowedUserIDs {
		allowed[id] = true
	}
	for _, id := range UserMentionIDs(text) {
		if allowed[id] {
			message.AllowedMentions.Users = append(message.AllowedMentions.Users, id)
		}
	}
	if replyToMessageID != "" {
		failIfMissing := false
		message.Reference = &discordgo.MessageReference{MessageID: replyToMessageID, ChannelID: channelID, FailIfNotExists: &failIfMissing}
	}
	if runURL != "" {
		divider, spacing := true, discordgo.SeparatorSpacingSizeSmall
		message.Components = append(message.Components,
			discordgo.Separator{Divider: &divider, Spacing: &spacing},
			discordgo.TextDisplay{Content: "-# [View in Blix](" + runURL + ")"},
		)
	}
	payload := struct {
		discordgo.MessageSend
		Nonce        string `json:"nonce"`
		EnforceNonce bool   `json:"enforce_nonce"`
	}{message, Nonce(id), true}
	endpoint := discordgo.EndpointChannelMessages(channelID)
	data, err := d.Session.RequestWithBucketID(http.MethodPost, endpoint, payload, endpoint, discordgo.WithContext(ctx))
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) {
			return "", &Error{Status: rest.Response.StatusCode, Code: "discord_delivery_failed"}
		}
		return "", &Error{Code: "discord_delivery_uncertain"}
	}
	var sent discordgo.Message
	if json.Unmarshal(data, &sent) != nil || sent.ID == "" {
		return "", &Error{Code: "discord_delivery_uncertain"}
	}
	return sent.ID, nil
}
