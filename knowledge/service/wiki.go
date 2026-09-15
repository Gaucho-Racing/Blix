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
)

//go:embed seed/*.md seed/team/*.md seed/seasons/*.md seed/seasons/gr26/*.md seed/seasons/gr27/*.md
var seedPages embed.FS

var seasonPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type WikiService struct {
	database *gorm.DB
	wiki     *repository.Wiki
	writer   sync.Mutex
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

func NewWikiService(database *gorm.DB, wiki *repository.Wiki) *WikiService {
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

func (s *WikiService) ListChangesets(ctx context.Context, limit int) ([]model.Changeset, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	results := []model.Changeset{}
	err := s.database.WithContext(ctx).Order("created_at DESC, id DESC").Limit(limit).Find(&results).Error
	return results, err
}

func (s *WikiService) ApplyChangeset(ctx context.Context, id string) (ChangesetDetails, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	return s.apply(ctx, id)
}

func (s *WikiService) apply(ctx context.Context, id string) (ChangesetDetails, error) {
	result, err := s.GetChangeset(ctx, id)
	if err != nil {
		return result, err
	}
	if result.Changeset.Status == model.ChangesetStatusCommitted {
		return result, nil
	}
	changeset := &result.Changeset
	if changeset.ResultCommitSHA == "" {
		edits := make([]repository.Edit, 0, len(result.Files))
		for _, file := range result.Files {
			edits = append(edits, repository.Edit{Operation: file.Operation, Path: file.Path, Content: file.Content})
		}
		sha, err := s.wiki.Prepare(ctx, changeset.BaseCommitSHA, changeset.ID, changeset.Summary, edits, changeset.CreatedAt)
		if err != nil {
			return result, s.recordFailure(ctx, changeset.ID, err)
		}
		if err := s.database.WithContext(ctx).Model(&model.Changeset{}).Where("id = ?", id).Updates(map[string]any{
			"result_commit_sha": sha, "status": model.ChangesetStatusPrepared, "error": "",
		}).Error; err != nil {
			return result, err
		}
		changeset.ResultCommitSHA = sha
	}
	if err := s.wiki.Publish(ctx, changeset.BaseCommitSHA, changeset.ResultCommitSHA); err != nil {
		return result, s.recordFailure(ctx, id, err)
	}
	now := time.Now().UTC()
	if err := s.database.WithContext(ctx).Model(&model.Changeset{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.ChangesetStatusCommitted, "committed_at": now, "error": "",
	}).Error; err != nil {
		return result, err
	}
	return s.GetChangeset(ctx, id)
}

func (s *WikiService) recordFailure(ctx context.Context, id string, cause error) error {
	err := s.database.WithContext(ctx).Model(&model.Changeset{}).Where("id = ?", id).Updates(map[string]any{
		"status": model.ChangesetStatusFailed, "error": cause.Error(),
	}).Error
	return errors.Join(cause, err)
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
