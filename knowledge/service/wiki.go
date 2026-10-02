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
}

type wikiRepository interface {
	Pages(context.Context, string, string) (repository.Snapshot, error)
	Search(context.Context, string, int) (repository.SearchResults, error)
	Prepare(context.Context, string, string, string, []repository.Edit, time.Time) (string, error)
	Publish(context.Context, string, string) error
	Refresh(context.Context) (string, error)
}

type Citation struct {
	SourceRecordID string    `json:"source_record_id"`
	SourceName     string    `json:"source_name"`
	RecordType     string    `json:"record_type"`
	ExternalURL    string    `json:"external_url"`
	Author         string    `json:"author,omitempty"`
	AuthorEntityID string    `json:"author_entity_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
}

type WikiPageResult struct {
	CommitSHA string              `json:"commit_sha"`
	Page      repository.Page     `json:"page"`
	Citations map[string]Citation `json:"citations"`
}

type WikiSearchResult struct {
	CommitSHA string                    `json:"commit_sha"`
	Results   []repository.SearchResult `json:"results"`
	Citations map[string]Citation       `json:"citations"`
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
}

func NewWikiService(database *gorm.DB, wiki wikiRepository) *WikiService {
	return &WikiService{database: database, wiki: wiki}
}

func (s *WikiService) ReadPage(ctx context.Context, path string) (WikiPageResult, error) {
	snapshot, err := s.wiki.Pages(ctx, path, "")
	if err != nil {
		return WikiPageResult{}, err
	}
	if len(snapshot.Pages) != 1 {
		return WikiPageResult{}, ErrNotFound
	}
	citations, err := s.resolveCitations(ctx, []string{snapshot.Pages[0].Content})
	return WikiPageResult{CommitSHA: snapshot.CommitSHA, Page: snapshot.Pages[0], Citations: citations}, err
}

func (s *WikiService) Search(ctx context.Context, query string, limit int) (WikiSearchResult, error) {
	results, err := s.wiki.Search(ctx, query, limit)
	if err != nil {
		return WikiSearchResult{}, err
	}
	contents := make([]string, 0, len(results.Results))
	for _, result := range results.Results {
		contents = append(contents, result.PageContent)
	}
	citations, err := s.resolveCitations(ctx, contents)
	return WikiSearchResult{CommitSHA: results.CommitSHA, Results: results.Results, Citations: citations}, err
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
	if err := s.database.WithContext(ctx).Where("id IN ?", ids).Find(&records).Error; err != nil {
		return nil, err
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
			ExternalURL: record.ExternalURL, OccurredAt: record.OccurredAt}
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
	if origin == "agent" && (len(input.SourceRecordIDs) == 0 || len(input.SourceRecordIDs) > 100) {
		return ChangesetDetails{}, fmt.Errorf("%w: provide between 1 and 100 source_record_ids", ErrInvalid)
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
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(input.SourceRecordIDs) > 0 {
			var count int64
			if err := tx.Model(&model.SourceRecord{}).Where("id IN ?", input.SourceRecordIDs).Count(&count).Error; err != nil {
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
			return tx.Create(&links).Error
		}
		return nil
	})
	return ChangesetDetails{Changeset: changeset, Files: files, SourceRecordIDs: input.SourceRecordIDs}, err
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

func (s *WikiService) applyReviewed(ctx context.Context, id string, review *ReviewChangesetInput) (ChangesetDetails, error) {
	for _, publish := range []bool{false, true} {
		var operationError error
		err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result, err := s.lockChangeset(ctx, tx, id)
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
			updates := map[string]any{}
			if review != nil && changeset.ReviewDecision != "approved" {
				updates = reviewUpdates(*review, "approved")
			}
			if publish {
				operationError = s.wiki.Publish(ctx, changeset.BaseCommitSHA, changeset.ResultCommitSHA)
				if operationError == nil {
					updates["status"], updates["committed_at"], updates["error"] = model.ChangesetStatusCommitted, time.Now().UTC(), ""
				}
			} else if changeset.ResultCommitSHA == "" {
				edits := make([]repository.Edit, 0, len(result.Files))
				for _, file := range result.Files {
					edits = append(edits, repository.Edit{Operation: file.Operation, Path: file.Path, Content: file.Content})
				}
				var sha string
				sha, operationError = s.wiki.Prepare(ctx, changeset.BaseCommitSHA, changeset.ID, changeset.Summary, edits, changeset.CreatedAt)
				if operationError == nil {
					updates["result_commit_sha"], updates["status"], updates["error"] = sha, model.ChangesetStatusPrepared, ""
				}
			}
			if operationError != nil {
				updates["status"], updates["error"] = model.ChangesetStatusFailed, operationError.Error()
			}
			if len(updates) == 0 {
				return nil
			}
			return tx.Model(&model.Changeset{}).Where("id = ?", id).Updates(updates).Error
		})
		if err != nil || operationError != nil {
			return ChangesetDetails{}, errors.Join(err, operationError)
		}
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
