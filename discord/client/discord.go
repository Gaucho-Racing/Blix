package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"unicode/utf16"

	"github.com/bwmarrin/discordgo"
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
func (d *Discord) Thread(ctx context.Context, channelID, messageID, name string) (*discordgo.Channel, error) {
	if ch, err := d.Channel(ctx, messageID); err == nil {
		if ch.ParentID != channelID || (ch.Type != discordgo.ChannelTypeGuildPublicThread && ch.Type != discordgo.ChannelTypeGuildNewsThread) {
			return nil, &Error{Status: 403, Code: "invalid_thread"}
		}
		return ch, nil
	} else {
		var rest *discordgo.RESTError
		if !errors.As(err, &rest) || rest.Response.StatusCode != 404 {
			return nil, err
		}
	}
	return d.Session.MessageThreadStartComplex(channelID, messageID, &discordgo.ThreadStart{Name: truncate(name, 90), AutoArchiveDuration: 1440}, discordgo.WithContext(ctx))
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
func (d *Discord) Send(ctx context.Context, id, channelID, content, runURL string) (string, error) {
	message := discordgo.MessageSend{Content: Preview(content), AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, Flags: discordgo.MessageFlagsSuppressEmbeds}
	if runURL != "" {
		message.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{Label: "View in Blix", Style: discordgo.LinkButton, URL: runURL}}}}
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
