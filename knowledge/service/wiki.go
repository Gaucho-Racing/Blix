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

type WikiService struct {
	database *gorm.DB
	wiki     wikiRepository
	writer   sync.Mutex
}

type wikiRepository interface {
	Prepare(context.Context, string, string, string, []repository.Edit, time.Time) (string, error)
	Publish(context.Context, string, string) error
	Refresh(context.Context) (string, error)
}

type CreateChangesetInput struct {
	RunID         string            `json:"run_id"`
	ExecutionID   string            `json:"execution_id"`
	SeasonSlug    string            `json:"season_slug"`
	Type          string            `json:"type"`
	Summary       string            `json:"summary"`
	BaseCommitSHA *string           `json:"base_commit_sha"`
	Files         []repository.Edit `json:"files"`
}

type ChangesetDetails struct {
	Changeset model.Changeset       `json:"changeset"`
	Files     []model.ChangesetFile `json:"files"`
}

func NewWikiService(database *gorm.DB, wiki wikiRepository) *WikiService {
	return &WikiService{database: database, wiki: wiki}
}

func (s *WikiService) CreateChangeset(ctx context.Context, input CreateChangesetInput) (ChangesetDetails, error) {
	if !identifier.IsValid(input.RunID, identifier.Run) || !identifier.IsValid(input.ExecutionID, identifier.Execution) {
		return ChangesetDetails{}, fmt.Errorf("%w: prefixed run_id and execution_id are required", ErrInvalid)
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
	changeset := model.Changeset{ID: identifier.New(identifier.Changeset), Origin: origin, RunID: input.RunID, ExecutionID: input.ExecutionID,
		SeasonSlug: input.SeasonSlug, Type: input.Type, Status: model.ChangesetStatusProposed, Summary: input.Summary, BaseCommitSHA: *input.BaseCommitSHA}
	files := make([]model.ChangesetFile, 0, len(input.Files))
	for _, edit := range input.Files {
		files = append(files, model.ChangesetFile{ID: identifier.New(identifier.ChangesetFile), ChangesetID: changeset.ID,
			Operation: edit.Operation, Path: edit.Path, Content: edit.Content, ContentHash: hashBytes([]byte(edit.Content))})
	}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&changeset).Error; err != nil {
			return err
		}
		return tx.Create(&files).Error
	})
	return ChangesetDetails{Changeset: changeset, Files: files}, err
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
	return result, err
}

type ChangesetPage struct {
	Changesets []model.Changeset `json:"changesets"`
	NextCursor string            `json:"next_cursor"`
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
