package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/client"
	"github.com/gaucho-racing/blix/discord/config"
	"github.com/gaucho-racing/blix/discord/model"
	"go.uber.org/zap"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IdentityResolver interface {
	Resolve(context.Context, string) (client.Identity, error)
}
type Runtime interface {
	Authorize(context.Context, client.Identity) error
	Submit(context.Context, client.Input, string) (client.InputResult, error)
	Events(context.Context, string, uint64) ([]client.Event, error)
}
type Transport interface {
	Channel(context.Context, string) (*discordgo.Channel, error)
	Guild(context.Context, string) (*discordgo.Guild, error)
	Thread(context.Context, string, string, string) (*discordgo.Channel, error)
	Send(context.Context, string, string, string, string) (string, error)
}
type Bridge struct {
	DB       *gorm.DB
	Config   config.Config
	Identity IdentityResolver
	Runtime  Runtime
	Discord  Transport
	Log      *zap.SugaredLogger
}

func (b *Bridge) Receive(ctx context.Context, botID string, message *discordgo.Message) error {
	if message == nil || message.Author == nil || message.Author.Bot || message.WebhookID != "" || message.GuildID != b.Config.GuildID || !config.Snowflake(message.ID) || !config.Snowflake(message.ChannelID) || !config.Snowflake(message.Author.ID) {
		return nil
	}
	mentioned := false
	for _, u := range message.Mentions {
		mentioned = mentioned || (u != nil && u.ID == botID)
	}
	if !mentioned || botID == "" {
		return nil
	}
	content := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(message.Content, "<@"+botID+">", ""), "<@!"+botID+">", ""))
	if content == "" {
		content = "[Empty mention]"
	}
	input := model.Inbox{ID: message.ID, GuildID: message.GuildID, ChannelID: message.ChannelID, AuthorID: message.Author.ID, Content: content, State: "pending", NextAttemptAt: time.Now().UTC()}
	return b.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&input).Error
}

func (b *Bridge) ProcessInbox(ctx context.Context) (bool, error) {
	var input model.Inbox
	err := b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state IN ? AND next_attempt_at <= ?", []string{"pending", "processing"}, time.Now()).Order("created_at,id").First(&input).Error; err != nil {
			return err
		}
		input.State = "processing"
		input.Attempts++
		return tx.Model(&input).Updates(map[string]any{"state": input.State, "attempts": input.Attempts, "next_attempt_at": time.Now().Add(time.Minute)}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = b.submit(ctx, &input)
	if err == nil {
		return true, b.DB.WithContext(ctx).Model(&input).Updates(map[string]any{"state": "completed", "error_code": ""}).Error
	}
	var upstream *client.Error
	if errors.As(err, &upstream) && !upstream.Retryable() {
		if upstream.Code == "channel_not_allowed" {
			return true, b.DB.WithContext(ctx).Model(&input).Updates(map[string]any{"state": "ignored", "error_code": upstream.Code}).Error
		}
		content := "I couldn’t start this request. Make sure your Discord account is linked in Sentinel and you have Blix access."
		if upstream.Code == "run_owner_required" {
			content = "This thread’s Blix run belongs to the person who started it. Mention me in the channel to start your own run."
		}
		if upstream.Code == "empty_question" {
			content = "Mention me with a question. Import documents from the Knowledge page in Blix."
		}
		if upstream.Code == "unsupported_channel" {
			content = "Mention me in a text channel or a public thread."
		}
		if upstream.Code == "no_active_run" {
			content = "There’s no active Blix run in this thread yet. Mention me with a question to start one."
		}
		return true, b.reject(ctx, input, upstream.Code, content)
	}
	if input.Attempts >= 10 {
		return true, b.reject(ctx, input, "retry_exhausted", "I couldn’t reach Blix to start this request. Please try again later.")
	}
	if updateErr := b.DB.WithContext(ctx).Model(&input).Updates(map[string]any{"state": "pending", "error_code": "upstream_unavailable", "next_attempt_at": time.Now().Add(backoff(input.Attempts))}).Error; updateErr != nil {
		return true, updateErr
	}
	return true, err
}

func (b *Bridge) submit(ctx context.Context, input *model.Inbox) error {
	channel, err := b.Discord.Channel(ctx, input.ChannelID)
	if err != nil {
		return err
	}
	if channel.GuildID != b.Config.GuildID {
		return &client.Error{Status: 403, Code: "channel_not_allowed"}
	}
	rootID := channel.ID
	isThread := channel.Type == discordgo.ChannelTypeGuildPublicThread || channel.Type == discordgo.ChannelTypeGuildNewsThread
	if isThread {
		rootID = channel.ParentID
	}
	if !isThread && channel.Type != discordgo.ChannelTypeGuildText && channel.Type != discordgo.ChannelTypeGuildNews {
		return &client.Error{Status: 400, Code: "unsupported_channel"}
	}
	if input.Content == "[Empty mention]" {
		return &client.Error{Status: 400, Code: "empty_question"}
	}
	identity, err := b.Identity.Resolve(ctx, input.AuthorID)
	if err != nil {
		return err
	}
	if identity.User == nil || identity.ID == "" || identity.User.EntityID != identity.ID {
		return &client.Error{Status: 403, Code: "invalid_linked_identity"}
	}
	if err = b.Runtime.Authorize(ctx, identity); err != nil {
		return err
	}
	if !isThread {
		channel, err = b.Discord.Thread(ctx, input.ChannelID, input.ID, "Blix · "+input.Content)
		if err != nil {
			return err
		}
		if channel.GuildID != b.Config.GuildID || channel.ParentID != rootID {
			return &client.Error{Status: 403, Code: "channel_not_allowed"}
		}
	}
	binding := model.Binding{ChannelID: channel.ID, GuildID: input.GuildID, RootChannelID: rootID, OwnerDiscordID: input.AuthorID, EntityID: identity.ID, UserID: identity.User.ID}
	if err = b.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
		return err
	}
	if err = b.DB.WithContext(ctx).First(&binding, "channel_id = ?", channel.ID).Error; err != nil {
		return err
	}
	if binding.OwnerDiscordID != input.AuthorID || binding.EntityID != identity.ID || binding.UserID != identity.User.ID {
		return &client.Error{Status: 403, Code: "run_owner_required"}
	}
	var payload client.Input
	if len(input.Payload) > 2 {
		if err = json.Unmarshal(input.Payload, &payload); err != nil {
			return err
		}
	} else {
		guild, err := b.Discord.Guild(ctx, input.GuildID)
		if err != nil {
			return err
		}
		root, err := b.Discord.Channel(ctx, rootID)
		if err != nil {
			return err
		}
		contextBytes, err := json.Marshal(map[string]any{"guild_id": guild.ID, "guild_name": guild.Name, "channel_id": root.ID, "channel_name": root.Name, "channel_topic": root.Topic, "thread_id": channel.ID, "thread_name": channel.Name, "discord_author_id": input.AuthorID, "sentinel_entity_id": identity.ID, "message_id": input.ID, "message_url": fmt.Sprintf("https://discord.com/channels/%s/%s/%s", input.GuildID, input.ChannelID, input.ID)})
		if err != nil {
			return err
		}
		payload = client.Input{Source: "discord", ExternalMessageID: input.ID, RunID: binding.RunID, Content: input.Content, Context: contextBytes}
		if strings.EqualFold(input.Content, "cancel") {
			payload.Operation = "cancel"
			if binding.RunID == "" {
				return &client.Error{Status: 400, Code: "no_active_run"}
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err = b.DB.WithContext(ctx).Model(input).Update("payload", datatypes.JSON(data)).Error; err != nil {
			return err
		}
	}
	var contextObject map[string]any
	if err := json.Unmarshal(payload.Context, &contextObject); err != nil {
		return err
	}
	canonicalContext, err := json.Marshal(contextObject)
	if err != nil {
		return err
	}
	payload.Context = canonicalContext
	result, err := b.Runtime.Submit(ctx, payload, identity.Token)
	if err != nil {
		return err
	}
	return b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&binding).Updates(map[string]any{"run_id": result.RunID, "session_id": result.SessionID}).Error; err != nil {
			return err
		}
		content := "On it. I’ll reply here when it’s ready."
		if payload.Operation == "cancel" {
			content = "Stopped the current work."
		}
		return queue(tx, model.Outbox{ID: "input/" + input.ID, ChannelID: binding.ChannelID, RunID: result.RunID, Content: content})
	})
}

func (b *Bridge) reject(ctx context.Context, input model.Inbox, code, content string) error {
	return b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&input).Updates(map[string]any{"state": "failed", "error_code": code}).Error; err != nil {
			return err
		}
		return queue(tx, model.Outbox{ID: "input/" + input.ID, ChannelID: input.ChannelID, Content: content})
	})
}
func queue(tx *gorm.DB, output model.Outbox) error {
	output.State = "pending"
	output.NextAttemptAt = time.Now().UTC()
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&output).Error
}
func backoff(attempt int) time.Duration { return time.Duration(1<<min(attempt, 6)) * time.Second }
