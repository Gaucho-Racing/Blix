package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gaucho-racing/blix/discord/client"
	"github.com/gaucho-racing/blix/discord/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (b *Bridge) PollEvents(ctx context.Context) error {
	var bindings []model.Binding
	if err := b.DB.WithContext(ctx).Where("run_id <> ''").Order("last_polled_at,id").Limit(25).Find(&bindings).Error; err != nil {
		return err
	}
	var failures []error
	for _, binding := range bindings {
		events, err := b.Runtime.Events(ctx, binding.RunID, binding.Cursor)
		if err == nil {
			err = b.SaveEvents(ctx, binding, events)
		}
		if err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}
func (b *Bridge) SaveEvents(ctx context.Context, binding model.Binding, events []client.Event) error {
	return b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Binding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", binding.ID).Error; err != nil {
			return err
		}
		if current.RunID != binding.RunID {
			return fmt.Errorf("run binding changed while polling")
		}
		cursor := current.Cursor
		for _, event := range events {
			if event.Position <= cursor {
				continue
			}
			if event.Position < binding.Cursor {
				return fmt.Errorf("invalid event cursor")
			}
			content := ""
			if event.SessionID == binding.SessionID {
				switch event.Type {
				case "message.assistant_v1":
					var payload struct {
						ChannelContent string `json:"channel_content"`
					}
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						return err
					}
					content = client.Preview(payload.ChannelContent)
				case "approval.requested_v1":
					content = "I need your approval before continuing. Open this run in Blix to review the action."
				case "attempt.failed_v1":
					content = "I couldn’t finish this request. Open Blix for the details."
				}
			}
			if content != "" {
				replyTo := binding.ID
				if event.TurnID != nil {
					var input model.Inbox
					err := tx.First(&input, "binding_id = ? AND turn_id = ?", binding.ID, *event.TurnID).Error
					if err == nil {
						replyTo = input.ID
					} else if !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
				}
				if err := queue(tx, model.Outbox{ID: fmt.Sprintf("event/%s/%d", binding.RunID, event.Position), ChannelID: binding.ChannelID, BindingID: binding.ID, ReplyToMessageID: replyTo, RunID: binding.RunID, Content: content}); err != nil {
					return err
				}
			}
			cursor = event.Position
		}
		return tx.Model(&current).Updates(map[string]any{"cursor": cursor, "last_polled_at": time.Now().UTC()}).Error
	})
}

func (b *Bridge) Deliver(ctx context.Context) (bool, error) {
	var output model.Outbox
	err := b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state IN ? AND next_attempt_at <= ?", []string{"pending", "sending"}, time.Now()).Order("created_at,id").First(&output).Error; err != nil {
			return err
		}
		if output.Reaction == "" && output.FirstAttemptAt != nil && time.Since(*output.FirstAttemptAt) > 2*time.Minute {
			output.State = "uncertain"
			return tx.Model(&output).Updates(map[string]any{"state": "uncertain", "error_code": "delivery_requires_reconciliation"}).Error
		}
		now := time.Now().UTC()
		if output.FirstAttemptAt == nil {
			output.FirstAttemptAt = &now
		}
		output.State = "sending"
		output.Attempts++
		return tx.Model(&output).Updates(map[string]any{"state": "sending", "attempts": output.Attempts, "first_attempt_at": output.FirstAttemptAt, "next_attempt_at": now.Add(time.Minute)}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if output.State == "uncertain" {
		return true, fmt.Errorf("Discord delivery %s requires reconciliation", output.ID)
	}
	runURL := ""
	if output.RunID != "" {
		runURL = strings.TrimRight(b.Config.PublicURL, "/") + "/chat/" + output.RunID
	}
	messageID := ""
	if output.Reaction != "" {
		err = b.Discord.React(ctx, output.ChannelID, output.ReplyToMessageID, output.Reaction)
	} else {
		allowedUserIDs := b.verifiedMentions(ctx, client.Preview(output.Content))
		messageID, err = b.Discord.Send(ctx, output.ID, output.ChannelID, output.ReplyToMessageID, output.Content, runURL, allowedUserIDs)
	}
	if err == nil {
		return true, b.DB.WithContext(ctx).Model(&output).Updates(map[string]any{"state": "sent", "discord_message_id": messageID, "error_code": ""}).Error
	}
	state, code := "pending", "discord_delivery_uncertain"
	var upstream *client.Error
	if errors.As(err, &upstream) {
		code = upstream.Code
		if !upstream.Retryable() {
			state = "failed"
		}
	}
	if updateErr := b.DB.WithContext(ctx).Model(&output).Updates(map[string]any{"state": state, "error_code": code, "next_attempt_at": time.Now().Add(backoff(output.Attempts))}).Error; updateErr != nil {
		return true, updateErr
	}
	return true, err
}

func (b *Bridge) verifiedMentions(ctx context.Context, content string) []string {
	ids := client.UserMentionIDs(content)
	verified := make([]bool, len(ids))
	limit := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for i, id := range ids {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				return
			}
			linked, err := b.Identity.VerifyDiscordMention(ctx, id)
			if err != nil {
				b.Log.Warnw("Discord mention lookup failed; notification disabled", "reason", safeError(err))
				return
			}
			verified[i] = linked
		}()
	}
	workers.Wait()
	allowed := []string{}
	for i, id := range ids {
		if verified[i] {
			allowed = append(allowed, id)
		}
	}
	return allowed
}
