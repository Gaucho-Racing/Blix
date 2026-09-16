package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IngestChatRecordInput struct {
	RunID       string    `json:"run_id"`
	ExecutionID string    `json:"execution_id"`
	MessageID   string    `json:"message_id"`
	EntityID    string    `json:"entity_id"`
	UserID      string    `json:"user_id"`
	Content     string    `json:"content"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func (s *SourceService) IngestChatRecord(ctx context.Context, input IngestChatRecordInput) (IngestedSourceRecord, error) {
	if !identifier.IsValid(input.RunID, identifier.Run) ||
		!identifier.IsValid(input.ExecutionID, identifier.Execution) ||
		!identifier.IsValid(input.MessageID, identifier.Message) ||
		!identifier.IsValid(input.EntityID, identifier.Entity) ||
		!identifier.IsValid(input.UserID, identifier.User) {
		return IngestedSourceRecord{}, fmt.Errorf("%w: valid run, execution, message, entity, and user IDs are required", ErrInvalid)
	}
	if strings.TrimSpace(input.Content) == "" || !utf8.ValidString(input.Content) ||
		strings.ContainsRune(input.Content, 0) || len(input.Content) > 1024*1024 || input.OccurredAt.IsZero() {
		return IngestedSourceRecord{}, fmt.Errorf("%w: timestamp and nonempty UTF-8 content up to 1 MiB are required", ErrInvalid)
	}
	metadata, err := json.Marshal(map[string]any{
		"run_id": input.RunID, "execution_id": input.ExecutionID, "message_id": input.MessageID,
		"evidence_type": "human_statement",
		"caller":        map[string]string{"entity_id": input.EntityID, "entity_type": "USER", "user_id": input.UserID},
	})
	if err != nil {
		return IngestedSourceRecord{}, err
	}
	var ingested IngestedSourceRecord
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source := model.Source{
			ID: identifier.New(identifier.Source), Kind: "blix_chat", ExternalID: input.RunID,
			Name: "Blix conversation " + input.RunID, Status: model.SourceStatusActive, Metadata: datatypes.JSON([]byte("{}")),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "kind"}, {Name: "external_id"}}, DoNothing: true,
		}).Create(&source).Error; err != nil {
			return err
		}
		var stored model.Source
		if err := tx.Where("kind = ? AND external_id = ?", "blix_chat", input.RunID).First(&stored).Error; err != nil {
			return err
		}
		var err error
		ingested, err = NewSourceService(tx).IngestSourceRecord(ctx, stored.ID, IngestSourceRecordInput{
			ExternalID: input.MessageID, ExternalVersion: "1", RecordType: "chat_message",
			ParentExternalID: input.RunID, Content: input.Content, Metadata: metadata, OccurredAt: input.OccurredAt,
			Author: &SourceIdentityInput{Provider: "sentinel", ExternalID: input.EntityID},
		})
		return err
	})
	return ingested, err
}
