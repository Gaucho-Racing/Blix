package service

import (
	"context"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/mqtt"
	"github.com/gaucho-racing/blix/runway/pkg/logger"
	"gorm.io/gorm"
)

type OutboxDispatcher struct {
	database     *gorm.DB
	publisher    *mqtt.Publisher
	pollInterval time.Duration
}

func NewOutboxDispatcher(database *gorm.DB, publisher *mqtt.Publisher, pollInterval time.Duration) *OutboxDispatcher {
	return &OutboxDispatcher{database: database, publisher: publisher, pollInterval: pollInterval}
}

func (d *OutboxDispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		d.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *OutboxDispatcher) dispatch(ctx context.Context) {
	retryBefore := time.Now().UTC().Add(-5 * time.Second)
	var messages []model.OutboxMessage
	if err := d.database.WithContext(ctx).
		Where("acknowledged_at IS NULL AND (last_published_at IS NULL OR last_published_at < ?)", retryBefore).
		Order("created_at ASC").
		Limit(100).
		Find(&messages).Error; err != nil {
		if ctx.Err() == nil {
			logger.SugarLogger.Errorf("failed to load outbox messages: %v", err)
		}
		return
	}
	for _, message := range messages {
		if err := d.publisher.Publish(ctx, message.Topic, message.Payload); err != nil {
			if ctx.Err() == nil {
				logger.SugarLogger.Errorf("failed to publish outbox message %s: %v", message.ID, err)
			}
			return
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"last_published_at": now,
			"publish_attempts":  gorm.Expr("publish_attempts + 1"),
		}
		if message.AcknowledgeOnPublish {
			updates["acknowledged_at"] = now
		}
		if err := d.database.WithContext(ctx).Model(&model.OutboxMessage{}).
			Where("id = ? AND acknowledged_at IS NULL", message.ID).
			Updates(updates).Error; err != nil {
			logger.SugarLogger.Errorf("failed to update outbox message %s: %v", message.ID, err)
		}
	}
}
