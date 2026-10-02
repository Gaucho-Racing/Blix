package service

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed seed/*.md seed/team/*.md seed/seasons/*.md seed/seasons/gr26/*.md seed/seasons/gr27/*.md
var seedPages embed.FS

var seasonPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var sourceRecordReferencePattern = regexp.MustCompile(`sourcerecord_[0-9a-hjkmnp-tv-z]{26}`)

type WikiService struct {
	database *gorm.DB
	wiki     wikiRepository
	writer   sync.Mutex
	index    *WikiIndex
}

type wikiRepository interface {
	Pages(context.Context, string, string) (repository.Snapshot, error)
	Prepare(context.Context, string, string, string, []repository.Edit, time.Time) (string, error)
	Publish(context.Context, string, string) error
	Refresh(context.Context) (string, error)
}

type Citation struct {
	SourceRecordID string    `json:"source_record_id"`
	DocumentID     string    `json:"document_id,omitempty"`
	PDFPage        int       `json:"pdf_page,omitempty"`
	SourceName     string    `json:"source_name"`
	RecordType     string    `json:"record_type"`
	ExternalURL    string    `json:"external_url"`
	Author         string    `json:"author,omitempty"`
	AuthorEntityID string    `json:"author_entity_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	EvidenceType   string    `json:"evidence_type"`
	CaptureMethod  string    `json:"capture_method"`
}

type CreateChangesetInput struct {
	ConversationID  string            `json:"conversation_id"`
	SessionID       string            `json:"session_id"`
	SeasonSlug      string            `json:"season_slug"`
	Type            string            `json:"type"`
	Summary         string            `json:"summary"`
	BaseCommitSHA   *string           `json:"base_commit_sha"`
	SourceRecordIDs []string          `json:"source_record_ids"`
	Files           []repository.Edit `json:"files"`
}

type ChangesetDetails struct {
	Changeset       model.Changeset       `json:"changeset"`
	Files           []model.ChangesetFile `json:"files"`
	SourceRecordIDs []string              `json:"source_record_ids"`
	Citations       map[string]Citation   `json:"citations"`
	Documents       []model.Document      `json:"documents"`
}

func NewWikiService(database *gorm.DB, wiki wikiRepository) *WikiService {
	return &WikiService{database: database, wiki: wiki}
}

func (s *WikiService) resolveCitations(ctx context.Context, contents []string) (map[string]Citation, error) {
	ids := make([]string, 0)
	seen := map[string]struct{}{}
	for _, content := range contents {
		for _, id := range sourceRecordReferencePattern.FindAllString(content, -1) {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	citations := make(map[string]Citation, len(ids))
	if len(ids) == 0 {
		return citations, nil
	}
	var records []model.SourceRecord
	if err := s.database.WithContext(ctx).Omit("content", "metadata").Where("id IN ?", ids).Find(&records).Error; err != nil {
		return nil, err
	}
	var documentPages []model.DocumentPage
	if err := s.database.WithContext(ctx).Where("source_record_id IN ? OR visual_source_record_id IN ?", ids, ids).Find(&documentPages).Error; err != nil {
		return nil, err
	}
	pageByRecord := make(map[string]model.DocumentPage, len(documentPages))
	for _, page := range documentPages {
		pageByRecord[page.SourceRecordID] = page
		if page.VisualSourceRecordID != "" {
			pageByRecord[page.VisualSourceRecordID] = page
		}
	}
	sourceIDs := make([]string, 0, len(records))
	identityIDs := make([]string, 0, len(records))
	for _, record := range records {
		sourceIDs = append(sourceIDs, record.SourceID)
		if record.AuthorSourceIdentityID != nil {
			identityIDs = append(identityIDs, *record.AuthorSourceIdentityID)
		}
	}
	var sources []model.Source
	if err := s.database.WithContext(ctx).Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
		return nil, err
	}
	var identities []model.SourceIdentity
	if len(identityIDs) > 0 {
		if err := s.database.WithContext(ctx).Where("id IN ?", identityIDs).Find(&identities).Error; err != nil {
			return nil, err
		}
	}
	sourceNames := make(map[string]string, len(sources))
	for _, source := range sources {
		sourceNames[source.ID] = source.Name
	}
	authors := make(map[string]model.SourceIdentity, len(identities))
	for _, identity := range identities {
		authors[identity.ID] = identity
	}
	for _, record := range records {
		citation := Citation{SourceRecordID: record.ID, SourceName: sourceNames[record.SourceID], RecordType: record.RecordType,
			ExternalURL: record.ExternalURL, OccurredAt: record.OccurredAt, EvidenceType: record.EvidenceType, CaptureMethod: record.CaptureMethod}
		if record.AuthorSourceIdentityID != nil {
			identity := authors[*record.AuthorSourceIdentityID]
			citation.Author = identity.DisplayName
			if citation.Author == "" {
				citation.Author = identity.Username
			}
			if identity.EntityID != nil {
				citation.AuthorEntityID = *identity.EntityID
			}
		}
		if page, ok := pageByRecord[record.ID]; ok {
			citation.DocumentID = page.DocumentID
			citation.PDFPage = page.Number
		}
		citations[record.ID] = citation
	}
	return citations, nil
}

func (s *WikiService) CreateChangeset(ctx context.Context, input CreateChangesetInput) (ChangesetDetails, error) {
	if !identifier.IsValid(input.ConversationID, identifier.Conversation) || !identifier.IsValid(input.SessionID, identifier.Session) {
		return ChangesetDetails{}, fmt.Errorf("%w: prefixed conversation_id and session_id are required", ErrInvalid)
	}
	return s.create(ctx, input, "agent")
}

func (s *WikiService) create(ctx context.Context, input CreateChangesetInput, origin string) (ChangesetDetails, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Summary == "" || len(input.Summary) > 500 || strings.ContainsAny(input.Summary, "\r\n\x00") {
		return ChangesetDetails{}, fmt.Errorf("%w: summary must be a single line up to 500 bytes", ErrInvalid)
	}
	if input.BaseCommitSHA == nil || !repository.ValidCommit(*input.BaseCommitSHA) {
		return ChangesetDetails{}, fmt.Errorf("%w: base_commit_sha is required; use an empty string for an empty repository", ErrInvalid)
	}
	if input.SeasonSlug != "" && !seasonPattern.MatchString(input.SeasonSlug) {
		return ChangesetDetails{}, ErrInvalid
	}
	if input.Type != model.ChangesetTypeContent && input.Type != model.ChangesetTypeStructural {
		return ChangesetDetails{}, ErrInvalid
	}
	if err := repository.ValidateEdits(input.Files); err != nil {
		return ChangesetDetails{}, err
	}
	if origin == "agent" && (len(input.SourceRecordIDs) == 0 || len(input.SourceRecordIDs) > 500) {
		return ChangesetDetails{}, fmt.Errorf("%w: provide between 1 and 500 source_record_ids", ErrInvalid)
	}
	seenSources := make(map[string]struct{}, len(input.SourceRecordIDs))
	for _, sourceRecordID := range input.SourceRecordIDs {
		if !identifier.IsValid(sourceRecordID, identifier.SourceRecord) {
			return ChangesetDetails{}, fmt.Errorf("%w: invalid source_record_id", ErrInvalid)
		}
		if _, exists := seenSources[sourceRecordID]; exists {
			return ChangesetDetails{}, fmt.Errorf("%w: duplicate source_record_id", ErrInvalid)
		}
		seenSources[sourceRecordID] = struct{}{}
	}
	for _, edit := range input.Files {
		if edit.Operation != "upsert" || origin != "agent" {
			continue
		}
		hasCitation := false
		for sourceRecordID := range seenSources {
			if strings.Contains(edit.Content, sourceRecordID) {
				hasCitation = true
				break
			}
		}
		if !hasCitation {
			return ChangesetDetails{}, fmt.Errorf("%w: every upserted page must cite a supplied source record", ErrInvalid)
		}
	}
	changeset := model.Changeset{ID: identifier.New(identifier.Changeset), Origin: origin, ConversationID: input.ConversationID, SessionID: input.SessionID,
		SeasonSlug: input.SeasonSlug, Type: input.Type, Status: model.ChangesetStatusProposed, Summary: input.Summary, BaseCommitSHA: *input.BaseCommitSHA}
	files := make([]model.ChangesetFile, 0, len(input.Files))
	for _, edit := range input.Files {
		files = append(files, model.ChangesetFile{ID: identifier.New(identifier.ChangesetFile), ChangesetID: changeset.ID,
			Operation: edit.Operation, Path: edit.Path, Content: edit.Content, ContentHash: hashBytes([]byte(edit.Content))})
	}
	links := make([]model.ChangesetSourceRecord, 0, len(input.SourceRecordIDs))
	for _, sourceRecordID := range input.SourceRecordIDs {
		links = append(links, model.ChangesetSourceRecord{ChangesetID: changeset.ID, SourceRecordID: sourceRecordID})
	}
	var referencedSources []string
	for source := range seenSources {
		referencedSources = append(referencedSources, source)
	}
	for _, file := range files {
		for _, source := range sourceRecordReferencePattern.FindAllString(file.Content, -1) {
			if _, exists := seenSources[source]; !exists {
				seenSources[source] = struct{}{}
				referencedSources = append(referencedSources, source)
			}
		}
	}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var docs []model.Document
		query := tx.Where("id IN (SELECT document_id FROM knowledge_document_page WHERE source_record_id IN ? OR visual_source_record_id IN ?)", referencedSources, referencedSources)
		if input.SessionID != "" {
			query = query.Or("session_id = ? AND conversation_id = ?", input.SessionID, input.ConversationID)
		}
		if err := query.Order("id").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&docs).Error; err != nil {
			return err
		}
		for _, doc := range docs {
			if doc.ReviewStatus == model.DocumentReviewRejected {
				return fmt.Errorf("%w: rejected document evidence cannot be proposed", ErrConflict)
			}
		}
		if len(input.SourceRecordIDs) > 0 {
			var count int64
			if err := tx.Model(&model.SourceRecord{}).Where("id IN ? AND source_id IN (SELECT id FROM knowledge_source WHERE status = ?)", input.SourceRecordIDs, model.SourceStatusActive).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(input.SourceRecordIDs)) {
				return fmt.Errorf("%w: source record not found", ErrNotFound)
			}
			if err := validateDiscordAttribution(tx, input.SourceRecordIDs, input.Files); err != nil {
				return err
			}
		}
		if err := tx.Create(&changeset).Error; err != nil {
			return err
		}
		if err := tx.Create(&files).Error; err != nil {
			return err
		}
		if len(links) > 0 {
			if err := tx.Create(&links).Error; err != nil {
				return err
			}
		}
		if len(docs) > 0 {
			documentLinks := make([]model.ChangesetDocument, 0, len(docs))
			for _, doc := range docs {
				documentLinks = append(documentLinks, model.ChangesetDocument{ChangesetID: changeset.ID, DocumentID: doc.ID})
			}
			return tx.Create(&documentLinks).Error
		}
		return nil
	})
	if err != nil {
		return ChangesetDetails{}, err
	}
	return s.GetChangeset(ctx, changeset.ID)
}

func validateDiscordAttribution(tx *gorm.DB, sourceRecordIDs []string, edits []repository.Edit) error {
	var identities []model.SourceIdentity
	if err := tx.Distinct("knowledge_source_identity.*").
		Table("knowledge_source_identity").
		Joins("JOIN knowledge_source_record ON knowledge_source_record.author_source_identity_id = knowledge_source_identity.id").
		Where("knowledge_source_record.id IN ? AND knowledge_source_identity.provider = ?", sourceRecordIDs, "discord").
		Find(&identities).Error; err != nil {
		return err
	}
	content := ""
	for _, edit := range edits {
		if edit.Operation == "upsert" {
			content += "\n" + edit.Content
		}
	}
	for _, identity := range identities {
		if identity.ResolutionStatus != model.IdentityResolutionResolved || identity.EntityID == nil ||
			strings.TrimSpace(identity.DisplayName) == "" {
			return fmt.Errorf("%w: Discord evidence has an unresolved Sentinel author", ErrInvalid)
		}
		reference := identity.DisplayName + " (" + *identity.EntityID + ")"
		if !strings.Contains(content, reference) {
			return fmt.Errorf("%w: wiki changes must cite Discord authors as %s", ErrInvalid, reference)
		}
	}
	return nil
}

func (s *WikiService) GetChangeset(ctx context.Context, id string) (ChangesetDetails, error) {
	if !identifier.IsValid(id, identifier.Changeset) {
		return ChangesetDetails{}, ErrInvalid
	}
	var result ChangesetDetails
	if err := s.database.WithContext(ctx).First(&result.Changeset, "id = ?", id).Error; err != nil {
		return result, mapDatabaseError(err)
	}
	err := s.database.WithContext(ctx).Where("changeset_id = ?", id).Order("path ASC").Find(&result.Files).Error
	if err != nil {
		return result, err
	}
	var links []model.ChangesetSourceRecord
	if err := s.database.WithContext(ctx).Where("changeset_id = ?", id).Order("source_record_id ASC").Find(&links).Error; err != nil {
		return result, err
	}
	for _, link := range links {
		result.SourceRecordIDs = append(result.SourceRecordIDs, link.SourceRecordID)
	}
	contents := []string{strings.Join(result.SourceRecordIDs, " ")}
	for _, file := range result.Files {
		contents = append(contents, file.Content)
	}
	result.Citations, err = s.resolveCitations(ctx, contents)
	if err != nil {
		return result, err
	}
	result.Documents = []model.Document{}
	err = changesetDocuments(s.database.WithContext(ctx), id).Order("id").Find(&result.Documents).Error
	return result, err
}

type ChangesetPage struct {
	Changesets []model.Changeset `json:"changesets"`
	NextCursor string            `json:"next_cursor"`
}

type PendingChangesetMatch struct {
	Changeset       model.Changeset `json:"changeset"`
	Paths           []string        `json:"paths"`
	SourceRecordIDs []string        `json:"source_record_ids"`
}

func (s *WikiService) SearchPendingChangesets(ctx context.Context, query string, limit int) ([]PendingChangesetMatch, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 256 || limit < 1 || limit > 20 {
		return nil, ErrInvalid
	}
	db := s.database.WithContext(ctx).Model(&model.Changeset{}).
		Where("status IN ?", []string{model.ChangesetStatusProposed, model.ChangesetStatusPrepared, model.ChangesetStatusFailed})
	for _, term := range strings.Fields(strings.ToLower(query)) {
		pattern := "%" + term + "%"
		db = db.Where(`LOWER(summary) LIKE ? OR EXISTS (
			SELECT 1 FROM knowledge_changeset_file
			WHERE knowledge_changeset_file.changeset_id = knowledge_changeset.id
			AND LOWER(knowledge_changeset_file.path || ' ' || knowledge_changeset_file.content) LIKE ?
		)`, pattern, pattern)
	}
	var changesets []model.Changeset
	if err := db.Order("created_at DESC").Limit(limit).Find(&changesets).Error; err != nil {
		return nil, err
	}
	matches := make([]PendingChangesetMatch, 0, len(changesets))
	for _, changeset := range changesets {
		details, err := s.GetChangeset(ctx, changeset.ID)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(details.Files))
		for _, file := range details.Files {
			paths = append(paths, file.Path)
		}
		matches = append(matches, PendingChangesetMatch{
			Changeset: changeset, Paths: paths, SourceRecordIDs: details.SourceRecordIDs,
		})
	}
	return matches, nil
}

func (s *WikiService) ListChangesets(ctx context.Context, limit int, status, before string) (ChangesetPage, error) {
	if limit < 1 || limit > 100 {
		return ChangesetPage{}, ErrInvalid
	}
	query := s.database.WithContext(ctx)
	if status != "" {
		switch status {
		case model.ChangesetStatusProposed, model.ChangesetStatusPrepared, model.ChangesetStatusCommitted, model.ChangesetStatusFailed, model.ChangesetStatusRejected:
			query = query.Where("status = ?", status)
		default:
			return ChangesetPage{}, ErrInvalid
		}
	}
	if before != "" {
		if !identifier.IsValid(before, identifier.Changeset) {
			return ChangesetPage{}, ErrInvalid
		}
		query = query.Where("id < ?", before)
	}
	results := []model.Changeset{}
	err := query.Order("id DESC").Limit(limit + 1).Find(&results).Error
	page := ChangesetPage{Changesets: results}
	if len(results) > limit {
		page.Changesets = results[:limit]
		page.NextCursor = results[limit-1].ID
	}
	return page, err
}

func (s *WikiService) ApplyChangeset(ctx context.Context, id string) (ChangesetDetails, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	return s.apply(ctx, id)
}

func (s *WikiService) apply(ctx context.Context, id string) (ChangesetDetails, error) {
	return s.applyReviewed(ctx, id, nil)
}

func (s *WikiService) applyReviewed(ctx context.Context, id string, review *ReviewInput) (ChangesetDetails, error) {
	if !identifier.IsValid(id, identifier.Changeset) {
		return ChangesetDetails{}, ErrInvalid
	}
	owner := identifier.New(identifier.Changeset)
	var result ChangesetDetails
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		docs, err := lockChangesetDocuments(tx, id)
		if err != nil {
			return err
		}
		result, err = s.lockChangeset(ctx, tx, id)
		if err != nil {
			return err
		}
		changeset := result.Changeset
		if changeset.Status == model.ChangesetStatusRejected {
			return fmt.Errorf("%w: rejected changesets cannot be published", ErrConflict)
		}
		if changeset.Status == model.ChangesetStatusCommitted {
			return nil
		}
		if changeset.PublicationLeaseUntil != nil && changeset.PublicationLeaseUntil.After(time.Now()) {
			return fmt.Errorf("%w: publication is already running", ErrConflict)
		}
		for _, doc := range docs {
			if doc.ReviewStatus == model.DocumentReviewRejected {
				return fmt.Errorf("%w: rejected document evidence cannot be published", ErrConflict)
			}
			if doc.ReviewStatus == model.DocumentReviewPending {
				return fmt.Errorf("%w: document ingestion must finish before publishing its changeset", ErrConflict)
			}
			if doc.ReviewStatus == model.DocumentReviewInReview {
				if review == nil {
					return fmt.Errorf("%w: documents in review require BlixAdmin approval", ErrForbidden)
				}
				if !documentProcessingComplete(doc.Status) {
					return fmt.Errorf("%w: document processing must finish before publishing its changeset", ErrConflict)
				}
			}
		}
		updates := map[string]any{"publication_owner": owner, "publication_lease_until": time.Now().Add(2 * time.Minute)}
		if review != nil && changeset.ReviewDecision != "approved" {
			for key, value := range reviewUpdates(*review, "approved") {
				updates[key] = value
			}
		}
		return tx.Model(&model.Changeset{}).Where("id = ?", id).Updates(updates).Error
	})
	if err != nil || result.Changeset.Status == model.ChangesetStatusCommitted {
		return result, err
	}
	persist := func(updates map[string]any) error {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.database.WithContext(saveCtx).Transaction(func(tx *gorm.DB) error {
			docs, err := lockChangesetDocuments(tx, id)
			if err != nil {
				return err
			}
			saved := tx.Model(&model.Changeset{}).Where("id = ? AND publication_owner = ? AND publication_lease_until > ?", id, owner, time.Now()).Updates(updates)
			if saved.Error != nil {
				return saved.Error
			}
			if saved.RowsAffected != 1 {
				return fmt.Errorf("%w: publication lease expired", ErrConflict)
			}
			if updates["status"] == model.ChangesetStatusCommitted && review != nil {
				for _, doc := range docs {
					if doc.ReviewStatus == model.DocumentReviewInReview {
						if err := tx.Model(&doc).Updates(documentApprovalUpdates(*review)).Error; err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
	}
	fail := func(operationError error) (ChangesetDetails, error) {
		saveError := persist(map[string]any{"status": model.ChangesetStatusFailed, "error": operationError.Error(), "publication_owner": "", "publication_lease_until": nil})
		return ChangesetDetails{}, errors.Join(operationError, saveError)
	}
	changeset := result.Changeset
	if changeset.ResultCommitSHA == "" {
		edits := make([]repository.Edit, 0, len(result.Files))
		for _, file := range result.Files {
			edits = append(edits, repository.Edit{Operation: file.Operation, Path: file.Path, Content: file.Content})
		}
		operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		sha, operationError := s.wiki.Prepare(operationCtx, changeset.BaseCommitSHA, changeset.ID, changeset.Summary, edits, changeset.CreatedAt)
		cancel()
		if operationError != nil {
			return fail(operationError)
		}
		if err := persist(map[string]any{"result_commit_sha": sha, "status": model.ChangesetStatusPrepared, "error": ""}); err != nil {
			return ChangesetDetails{}, err
		}
		changeset.ResultCommitSHA = sha
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	operationError := s.wiki.Publish(operationCtx, changeset.BaseCommitSHA, changeset.ResultCommitSHA)
	cancel()
	if operationError != nil {
		return fail(operationError)
	}
	if err := persist(map[string]any{"status": model.ChangesetStatusCommitted, "committed_at": time.Now().UTC(), "error": "", "publication_owner": "", "publication_lease_until": nil}); err != nil {
		return ChangesetDetails{}, err
	}
	if s.index != nil {
		s.index.Wake()
	}
	return s.GetChangeset(ctx, id)
}

func (s *WikiService) lockChangeset(ctx context.Context, tx *gorm.DB, id string) (ChangesetDetails, error) {
	if !identifier.IsValid(id, identifier.Changeset) {
		return ChangesetDetails{}, ErrInvalid
	}
	var changeset model.Changeset
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&changeset, "id = ?", id).Error; err != nil {
		return ChangesetDetails{}, mapDatabaseError(err)
	}
	return NewWikiService(tx, s.wiki).GetChangeset(ctx, id)
}

func (s *WikiService) Seed(ctx context.Context) (ChangesetDetails, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	var existing model.Changeset
	err := s.database.WithContext(ctx).Where("origin = ?", "bootstrap").Order("created_at ASC").First(&existing).Error
	if err == nil {
		return s.apply(ctx, existing.ID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ChangesetDetails{}, err
	}
	head, err := s.wiki.Refresh(ctx)
	if err != nil {
		return ChangesetDetails{}, err
	}
	if head != "" {
		return ChangesetDetails{}, fmt.Errorf("%w: seeding requires an empty repository", ErrConflict)
	}
	edits := []repository.Edit{}
	err = fs.WalkDir(seedPages, "seed", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := seedPages.ReadFile(name)
		if err != nil {
			return err
		}
		edits = append(edits, repository.Edit{Operation: "upsert", Path: strings.TrimPrefix(name, "seed/"), Content: string(content)})
		return nil
	})
	if err != nil {
		return ChangesetDetails{}, err
	}
	result, err := s.create(ctx, CreateChangesetInput{Type: model.ChangesetTypeStructural, Summary: "docs: seed Blix knowledge wiki", BaseCommitSHA: &head, Files: edits}, "bootstrap")
	if err != nil {
		return result, err
	}
	return s.apply(ctx, result.Changeset.ID)
}
