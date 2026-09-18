package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	Kind       string          `json:"kind"`
	ExternalID string          `json:"external_id"`
	Name       string          `json:"name"`
	Metadata   json.RawMessage `json:"metadata"`
}

type SourceIdentityInput struct {
	Provider      string  `json:"provider"`
	ExternalID    string  `json:"external_id"`
	DisplayName   string  `json:"display_name"`
	Username      string  `json:"username"`
	EntityID      *string `json:"entity_id"`
	UserID        *string `json:"user_id"`
	CanonicalName string  `json:"canonical_name"`
}

type IngestSourceRecordInput struct {
	ExternalID         string               `json:"external_id"`
	ExternalVersion    string               `json:"external_version"`
	RecordType         string               `json:"record_type"`
	ParentExternalID   string               `json:"parent_external_id"`
	ExternalURL        string               `json:"external_url"`
	Content            string               `json:"content"`
	Metadata           json.RawMessage      `json:"metadata"`
	OccurredAt         time.Time            `json:"occurred_at"`
	Author             *SourceIdentityInput `json:"author"`
	EvidenceType       string               `json:"evidence_type"`
	CaptureMethod      string               `json:"capture_method"`
	CaptureRunID       string               `json:"capture_run_id"`
	CaptureExecutionID string               `json:"capture_execution_id"`
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
	if !sourceKindPattern.MatchString(input.Kind) || len(input.Kind) > 64 || input.ExternalID == "" || len(input.ExternalID) > 512 || input.Name == "" || len(input.Name) > 512 {
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
	if err := s.database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "kind"}, {Name: "external_id"}}, DoNothing: true,
	}).Create(&source).Error; err != nil {
		return model.Source{}, mapDatabaseError(err)
	}
	var stored model.Source
	if err := s.database.WithContext(ctx).Where("kind = ? AND external_id = ?", input.Kind, input.ExternalID).First(&stored).Error; err != nil {
		return model.Source{}, mapDatabaseError(err)
	}
	if stored.Status != model.SourceStatusActive {
		return model.Source{}, fmt.Errorf("%w: source is disabled", ErrConflict)
	}
	return stored, nil
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
	if len(input.ExternalID) > 512 || len(input.ExternalVersion) > 512 || len(input.ParentExternalID) > 512 || len(input.RecordType) > 64 ||
		len(input.Content) > 1024*1024 || !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, 0) {
		return IngestedSourceRecord{}, fmt.Errorf("%w: invalid record size or text encoding", ErrInvalid)
	}
	if input.ExternalURL != "" {
		parsed, err := url.Parse(input.ExternalURL)
		if err != nil || len(input.ExternalURL) > 4096 || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return IngestedSourceRecord{}, fmt.Errorf("%w: external_url must be an HTTP(S) URL without credentials", ErrInvalid)
		}
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
			EvidenceType:           input.EvidenceType,
			CaptureMethod:          input.CaptureMethod,
			CaptureRunID:           input.CaptureRunID,
			CaptureExecutionID:     input.CaptureExecutionID,
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
	input.CanonicalName = strings.TrimSpace(input.CanonicalName)
	if !sourceKindPattern.MatchString(input.Provider) || input.ExternalID == "" {
		return nil, fmt.Errorf("%w: author provider or external_id is invalid", ErrInvalid)
	}
	resolved := input.EntityID != nil || input.UserID != nil || input.CanonicalName != ""
	if resolved && (input.EntityID == nil || input.UserID == nil || input.CanonicalName == "" ||
		!identifier.IsValid(*input.EntityID, identifier.Entity) || !identifier.IsValid(*input.UserID, identifier.User)) {
		return nil, fmt.Errorf("%w: resolved author identity is incomplete or invalid", ErrInvalid)
	}
	if input.Provider == "discord" && !resolved {
		return nil, fmt.Errorf("%w: Discord authors require a resolved Sentinel user", ErrInvalid)
	}
	resolutionStatus := model.IdentityResolutionUnresolved
	resolutionMethod := ""
	var resolvedAt *time.Time
	if resolved {
		now := time.Now().UTC()
		resolvedAt = &now
		resolutionStatus = model.IdentityResolutionResolved
		resolutionMethod = "sentinel_external_identity"
		input.DisplayName = input.CanonicalName
	}
	identity := model.SourceIdentity{
		ID:               identifier.New(identifier.SourceIdentity),
		Provider:         input.Provider,
		ExternalID:       input.ExternalID,
		DisplayName:      input.DisplayName,
		Username:         input.Username,
		EntityID:         input.EntityID,
		UserID:           input.UserID,
		ResolutionStatus: resolutionStatus,
		ResolutionMethod: resolutionMethod,
		ResolvedAt:       resolvedAt,
		LastObservedAt:   observedAt.UTC(),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "external_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"display_name":      identity.DisplayName,
			"username":          identity.Username,
			"entity_id":         identity.EntityID,
			"user_id":           identity.UserID,
			"resolution_status": identity.ResolutionStatus,
			"resolution_method": identity.ResolutionMethod,
			"resolved_at":       identity.ResolvedAt,
			"last_observed_at":  identity.LastObservedAt,
			"updated_at":        time.Now().UTC(),
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
	if len(value) > 65536 {
		return nil, fmt.Errorf("%w: metadata exceeds 64 KiB", ErrInvalid)
	}
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
		EvidenceType     string          `json:"evidence_type,omitempty"`
		CaptureMethod    string          `json:"capture_method,omitempty"`
	}{
		RecordType:       input.RecordType,
		ParentExternalID: input.ParentExternalID,
		AuthorID:         authorID,
		ExternalURL:      input.ExternalURL,
		Content:          input.Content,
		Metadata:         json.RawMessage(metadata),
		OccurredAt:       input.OccurredAt.UTC(),
		EvidenceType:     input.EvidenceType,
		CaptureMethod:    input.CaptureMethod,
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
