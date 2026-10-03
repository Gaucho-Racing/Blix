package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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
	VerifyDiscordMention(context.Context, string) (bool, error)
}
type Runtime interface {
	Authorize(context.Context, client.Identity) error
	Submit(context.Context, client.Input, string) (client.InputResult, error)
	Events(context.Context, string, uint64) ([]client.Event, error)
}
type Transport interface {
	Channel(context.Context, string) (*discordgo.Channel, error)
	Guild(context.Context, string) (*discordgo.Guild, error)
	Send(context.Context, string, string, string, string, string, []string) (string, error)
	React(context.Context, string, string, string) error
	RemoveReaction(context.Context, string, string, string) error
	Delete(context.Context, string, string) error
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
	if ref := message.MessageReference; ref != nil && config.Snowflake(ref.MessageID) && (ref.ChannelID == "" || ref.ChannelID == message.ChannelID) && (ref.GuildID == "" || ref.GuildID == message.GuildID) {
		input.ReplyToMessageID = ref.MessageID
	}
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
			content = "This Blix run belongs to the person who started it. Mention me in a new message to start your own run."
		}
		if upstream.Code == "empty_question" {
			content = "Hey! How can I help?"
		}
		if upstream.Code == "unsupported_channel" {
			content = "Mention me in a text channel or a public thread."
		}
		if upstream.Code == "no_active_run" {
			content = "Reply to a message from your Blix run and mention me with cancel to stop it."
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
	bindingID, err := b.bindingID(ctx, input)
	if err != nil {
		return err
	}
	if strings.EqualFold(input.Content, "cancel") && bindingID == input.ID {
		return &client.Error{Status: 400, Code: "no_active_run"}
	}
	binding := model.Binding{ID: bindingID, ChannelID: channel.ID, GuildID: input.GuildID, RootChannelID: rootID, OwnerDiscordID: input.AuthorID, EntityID: identity.ID, UserID: identity.User.ID}
	if err = b.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
		return err
	}
	if err = b.DB.WithContext(ctx).First(&binding, "id = ?", bindingID).Error; err != nil {
		return err
	}
	if binding.ChannelID != input.ChannelID || binding.GuildID != input.GuildID || binding.OwnerDiscordID != input.AuthorID || binding.EntityID != identity.ID || binding.UserID != identity.User.ID {
		return &client.Error{Status: 403, Code: "run_owner_required"}
	}
	if binding.RunID == "" && binding.ID != input.ID {
		return &client.Error{Code: "parent_request_pending"}
	}
	if err = b.DB.WithContext(ctx).Model(input).Update("binding_id", binding.ID).Error; err != nil {
		return err
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
		transportContext := map[string]any{"guild_id": guild.ID, "guild_name": guild.Name, "channel_id": root.ID, "channel_name": root.Name, "channel_topic": root.Topic, "discord_author_id": input.AuthorID, "sentinel_entity_id": identity.ID, "message_id": input.ID, "reply_to_message_id": input.ReplyToMessageID, "message_url": fmt.Sprintf("https://discord.com/channels/%s/%s/%s", input.GuildID, input.ChannelID, input.ID)}
		if isThread {
			transportContext["thread_id"], transportContext["thread_name"] = channel.ID, channel.Name
		}
		contextBytes, err := json.Marshal(transportContext)
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
		if err := tx.Model(input).Update("turn_id", result.TurnID).Error; err != nil {
			return err
		}
		output := model.Outbox{ID: "input/" + input.ID, ChannelID: binding.ChannelID, BindingID: binding.ID, ReplyToMessageID: input.ID, RunID: result.RunID, Content: workingReplies[rand.IntN(len(workingReplies))], Placeholder: true}
		if payload.Operation == "cancel" {
			output.Placeholder, output.Content = false, "Stopped the current work."
			var placeholders []model.Outbox
			if err := tx.Where("binding_id = ? AND placeholder = true AND state IN ?", binding.ID, []string{"pending", "sending", "sent"}).Find(&placeholders).Error; err != nil {
				return err
			}
			for _, placeholder := range placeholders {
				if err := queuePlaceholderCleanup(tx, output.ID+"/cleanup/"+placeholder.ID, placeholder); err != nil {
					return err
				}
				if err := queueEyesRemoval(tx, placeholder); err != nil {
					return err
				}
			}
		} else {
			eyes := model.Outbox{ID: "eyes/add/" + input.ID, ChannelID: binding.ChannelID, ReplyToMessageID: input.ID, Reaction: "👀"}
			if err := queue(tx, eyes); err != nil {
				return err
			}
			output.WaitForOutboxID = eyes.ID
			output.NextAttemptAt = input.CreatedAt.Add(placeholderDelay)
		}
		return queue(tx, output)
	})
}

func (b *Bridge) reject(ctx context.Context, input model.Inbox, code, content string) error {
	return b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&input).Updates(map[string]any{"state": "failed", "error_code": code}).Error; err != nil {
			return err
		}
		return queue(tx, model.Outbox{ID: "input/" + input.ID, ChannelID: input.ChannelID, ReplyToMessageID: input.ID, Content: content})
	})
}

func (b *Bridge) bindingID(ctx context.Context, input *model.Inbox) (string, error) {
	if input.BindingID != "" {
		return input.BindingID, nil
	}
	if input.ReplyToMessageID == "" {
		return input.ID, nil
	}
	var parent model.Inbox
	err := b.DB.WithContext(ctx).First(&parent, "id = ? AND channel_id = ?", input.ReplyToMessageID, input.ChannelID).Error
	if err == nil {
		if parent.BindingID != "" {
			return parent.BindingID, nil
		}
		if parent.State == "pending" || parent.State == "processing" {
			return "", &client.Error{Code: "parent_request_pending"}
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	var output model.Outbox
	err = b.DB.WithContext(ctx).First(&output, "discord_message_id = ? AND channel_id = ? AND binding_id <> ''", input.ReplyToMessageID, input.ChannelID).Error
	if err == nil {
		return output.BindingID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	return input.ID, nil
}
func queue(tx *gorm.DB, output model.Outbox) error {
	output.State = "pending"
	if output.NextAttemptAt.IsZero() {
		output.NextAttemptAt = time.Now().UTC()
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&output).Error
}

var workingReplies = [...]string{
	"Working on it!",
	"On it!",
	"Let me take a look.",
	"Looking into it!",
	"Give me a moment to check.",
}

const placeholderDelay = 6 * time.Second

func queueEyesRemoval(tx *gorm.DB, output model.Outbox) error {
	var eyes model.Outbox
	err := tx.First(&eyes, "id = ? AND channel_id = ?", "eyes/add/"+output.ReplyToMessageID, output.ChannelID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Model(&eyes).Where("state = 'pending'").Update("state", "cancelled").Error; err != nil {
		return err
	}
	return queue(tx, model.Outbox{ID: "eyes/remove/" + output.ReplyToMessageID, ChannelID: output.ChannelID, ReplyToMessageID: output.ReplyToMessageID, Reaction: "👀", RemoveReaction: true, WaitForOutboxID: eyes.ID})
}

func queueResponse(tx *gorm.DB, output model.Outbox) error {
	var placeholder model.Outbox
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&placeholder, "id = ? AND placeholder = true", "input/"+output.ReplyToMessageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return queue(tx, output)
	}
	if err != nil {
		return err
	}
	output.WaitForOutboxID = output.ID + "/cleanup"
	if err := queuePlaceholderCleanup(tx, output.WaitForOutboxID, placeholder); err != nil {
		return err
	}
	return queue(tx, output)
}

func queuePlaceholderCleanup(tx *gorm.DB, id string, placeholder model.Outbox) error {
	if err := tx.Model(&placeholder).Where("state = 'pending' AND first_attempt_at IS NULL").Update("state", "cancelled").Error; err != nil {
		return err
	}
	return queue(tx, model.Outbox{ID: id, ChannelID: placeholder.ChannelID, DeleteOutboxID: placeholder.ID, WaitForOutboxID: placeholder.ID})
}
func backoff(attempt int) time.Duration { return time.Duration(1<<min(attempt, 6)) * time.Second }
