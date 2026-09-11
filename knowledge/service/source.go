package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var sourceKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type SourceService struct {
	database *gorm.DB
}

type CreateSourceInput struct {
	Kind       string
	ExternalID string
	Name       string
	Metadata   json.RawMessage
}

type SourceIdentityInput struct {
	Provider    string
	ExternalID  string
	DisplayName string
	Username    string
}

type IngestSourceRecordInput struct {
	ExternalID       string
	ExternalVersion  string
	RecordType       string
	ParentExternalID string
	ExternalURL      string
	Content          string
	Metadata         json.RawMessage
	OccurredAt       time.Time
	Author           *SourceIdentityInput
}

type IngestedSourceRecord struct {
	Record  model.SourceRecord `json:"record"`
	Created bool               `json:"created"`
}

func NewSourceService(database *gorm.DB) *SourceService {
	return &SourceService{database: database}
}

func (s *SourceService) CreateSource(ctx context.Context, input CreateSourceInput) (model.Source, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.Name = strings.TrimSpace(input.Name)
	if !sourceKindPattern.MatchString(input.Kind) || input.ExternalID == "" || input.Name == "" {
		return model.Source{}, fmt.Errorf("%w: kind, external_id, or name is invalid", ErrInvalid)
	}
	metadata, err := normalizeMetadata(input.Metadata)
	if err != nil {
		return model.Source{}, err
	}
	source := model.Source{
		ID:         identifier.New(identifier.Source),
		Kind:       input.Kind,
		ExternalID: input.ExternalID,
		Name:       input.Name,
		Status:     model.SourceStatusActive,
		Metadata:   metadata,
	}
	if err := s.database.WithContext(ctx).Create(&source).Error; err != nil {
		return model.Source{}, mapDatabaseError(err)
	}
	return source, nil
}

func (s *SourceService) ListSources(ctx context.Context, limit int) ([]model.Source, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalid)
	}
	var sources []model.Source
	err := s.database.WithContext(ctx).Order("created_at DESC, id DESC").Limit(limit).Find(&sources).Error
	return sources, err
}

func (s *SourceService) GetSourceRecord(ctx context.Context, recordID string) (model.SourceRecord, error) {
	if !identifier.IsValid(recordID, identifier.SourceRecord) {
		return model.SourceRecord{}, fmt.Errorf("%w: source_record_id is invalid", ErrInvalid)
	}
	var record model.SourceRecord
	if err := s.database.WithContext(ctx).First(&record, "id = ?", recordID).Error; err != nil {
		return model.SourceRecord{}, mapDatabaseError(err)
	}
	return record, nil
}

func (s *SourceService) IngestSourceRecord(
	ctx context.Context,
	sourceID string,
	input IngestSourceRecordInput,
) (IngestedSourceRecord, error) {
	if !identifier.IsValid(sourceID, identifier.Source) {
		return IngestedSourceRecord{}, fmt.Errorf("%w: source_id is invalid", ErrInvalid)
	}
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.ExternalVersion = strings.TrimSpace(input.ExternalVersion)
	input.RecordType = strings.TrimSpace(input.RecordType)
	input.ParentExternalID = strings.TrimSpace(input.ParentExternalID)
	input.ExternalURL = strings.TrimSpace(input.ExternalURL)
	if input.ExternalID == "" || !sourceKindPattern.MatchString(input.RecordType) || input.OccurredAt.IsZero() {
		return IngestedSourceRecord{}, fmt.Errorf("%w: external_id, record_type, or occurred_at is invalid", ErrInvalid)
	}
	metadata, err := normalizeMetadata(input.Metadata)
	if err != nil {
		return IngestedSourceRecord{}, err
	}
	var ingested IngestedSourceRecord
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source model.Source
		if err := tx.First(&source, "id = ?", sourceID).Error; err != nil {
			return mapDatabaseError(err)
		}
		if source.Status != model.SourceStatusActive {
			return ErrConflict
		}
		authorID, err := upsertSourceIdentity(tx, input.Author, input.OccurredAt)
		if err != nil {
			return err
		}
		contentHash := hashBytes([]byte(input.Content))
		recordHash, err := hashRecord(input, metadata, authorID)
		if err != nil {
			return err
		}
		externalVersion := input.ExternalVersion
		if externalVersion == "" {
			externalVersion = recordHash
		}
		record := model.SourceRecord{
			ID:                     identifier.New(identifier.SourceRecord),
			SourceID:               sourceID,
			ExternalID:             input.ExternalID,
			ExternalVersion:        externalVersion,
			RecordType:             input.RecordType,
			ParentExternalID:       input.ParentExternalID,
			AuthorSourceIdentityID: authorID,
			ExternalURL:            input.ExternalURL,
			Content:                input.Content,
			ContentHash:            contentHash,
			RecordHash:             recordHash,
			Metadata:               metadata,
			OccurredAt:             input.OccurredAt.UTC(),
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}, {Name: "external_id"}, {Name: "external_version"}},
			DoNothing: true,
		}).Create(&record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			ingested = IngestedSourceRecord{Record: record, Created: true}
			return nil
		}
		var existing model.SourceRecord
		if err := tx.Where(
			"source_id = ? AND external_id = ? AND external_version = ?",
			sourceID,
			input.ExternalID,
			externalVersion,
		).First(&existing).Error; err != nil {
			return err
		}
		if existing.RecordHash != recordHash {
			return fmt.Errorf("%w: immutable source record version has different content", ErrConflict)
		}
		ingested = IngestedSourceRecord{Record: existing, Created: false}
		return nil
	})
	if err != nil {
		return IngestedSourceRecord{}, err
	}
	return ingested, nil
}

func upsertSourceIdentity(
	tx *gorm.DB,
	input *SourceIdentityInput,
	observedAt time.Time,
) (*string, error) {
	if input == nil {
		return nil, nil
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Username = strings.TrimSpace(input.Username)
	if !sourceKindPattern.MatchString(input.Provider) || input.ExternalID == "" {
		return nil, fmt.Errorf("%w: author provider or external_id is invalid", ErrInvalid)
	}
	identity := model.SourceIdentity{
		ID:               identifier.New(identifier.SourceIdentity),
		Provider:         input.Provider,
		ExternalID:       input.ExternalID,
		DisplayName:      input.DisplayName,
		Username:         input.Username,
		ResolutionStatus: model.IdentityResolutionUnresolved,
		ResolutionMethod: "",
		LastObservedAt:   observedAt.UTC(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "external_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"display_name":     identity.DisplayName,
			"username":         identity.Username,
			"last_observed_at": identity.LastObservedAt,
			"updated_at":       time.Now().UTC(),
		}),
	}).Create(&identity).Error; err != nil {
		return nil, err
	}
	var stored model.SourceIdentity
	if err := tx.Where("provider = ? AND external_id = ?", input.Provider, input.ExternalID).First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored.ID, nil
}

func normalizeMetadata(value json.RawMessage) (datatypes.JSON, error) {
	if len(value) == 0 || string(value) == "null" {
		return datatypes.JSON([]byte("{}")), nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(value, &metadata); err != nil {
		return nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
	}
	normalized, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(normalized), nil
}

func hashRecord(input IngestSourceRecordInput, metadata datatypes.JSON, authorID *string) (string, error) {
	fingerprint := struct {
		RecordType       string          `json:"record_type"`
		ParentExternalID string          `json:"parent_external_id"`
		AuthorID         *string         `json:"author_id"`
		ExternalURL      string          `json:"external_url"`
		Content          string          `json:"content"`
		Metadata         json.RawMessage `json:"metadata"`
		OccurredAt       time.Time       `json:"occurred_at"`
	}{
		RecordType:       input.RecordType,
		ParentExternalID: input.ParentExternalID,
		AuthorID:         authorID,
		ExternalURL:      input.ExternalURL,
		Content:          input.Content,
		Metadata:         json.RawMessage(metadata),
		OccurredAt:       input.OccurredAt.UTC(),
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func hashBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
