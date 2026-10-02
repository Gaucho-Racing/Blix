package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type publishingRepository struct {
	started    chan struct{}
	release    chan struct{}
	publishErr error
	prepared   int
	published  int
}

func (r *publishingRepository) Pages(context.Context, string, string) (repository.Snapshot, error) {
	return repository.Snapshot{}, nil
}
func (r *publishingRepository) Refresh(context.Context) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (r *publishingRepository) Prepare(ctx context.Context, base, id, summary string, edits []repository.Edit, when time.Time) (string, error) {
	r.prepared++
	if r.started != nil {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return strings.Repeat("b", 40), nil
}
func (r *publishingRepository) Publish(context.Context, string, string) error {
	r.published++
	return r.publishErr
}
func publishFixture(t *testing.T, db *gorm.DB) string {
	t.Helper()
	if err := db.Exec("TRUNCATE knowledge_changeset, knowledge_changeset_file, knowledge_changeset_source_record").Error; err != nil {
		t.Fatal(err)
	}
	id := identifier.New(identifier.Changeset)
	changeset := model.Changeset{ID: id, Status: model.ChangesetStatusProposed, Type: model.ChangesetTypeContent, Summary: "Update brakes", BaseCommitSHA: strings.Repeat("a", 40)}
	if err := db.Create(&changeset).Error; err != nil {
		t.Fatal(err)
	}
	file := model.ChangesetFile{ID: identifier.New(identifier.ChangesetFile), ChangesetID: id, Operation: "upsert", Path: "brakes.md", Content: "# Brakes"}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublicationLeaseReleasesDatabaseLocks(t *testing.T) {
	db, index, _, _ := indexFixture(t)
	id := publishFixture(t, db)
	repo := &publishingRepository{started: make(chan struct{}), release: make(chan struct{})}
	publisher := NewWikiService(db, repo)
	publisher.SetIndex(index)
	done := make(chan error, 1)
	go func() { _, err := publisher.ApplyChangeset(context.Background(), id); done <- err }()
	select {
	case <-repo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not start")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '200ms'").Error; err != nil {
			return err
		}
		var changeset model.Changeset
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&changeset, "id = ?", id).Error
	})
	if err != nil {
		close(repo.release)
		t.Fatalf("network operation held database lock: %v", err)
	}
	competing := NewWikiService(db, repo)
	if _, err := competing.ApplyChangeset(context.Background(), id); !errors.Is(err, ErrConflict) {
		close(repo.release)
		t.Fatal("competing publisher bypassed lease")
	}
	if _, err := competing.ReviewChangeset(context.Background(), id, "reject", ReviewChangesetInput{ReviewerEntityID: identifier.New(identifier.Entity), Note: "Reject"}); !errors.Is(err, ErrConflict) {
		close(repo.release)
		t.Fatal("in-flight publication could be rejected")
	}
	close(repo.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := publisher.ApplyChangeset(context.Background(), id)
	if err != nil || result.Changeset.Status != model.ChangesetStatusCommitted || repo.prepared != 1 || repo.published != 1 {
		t.Fatal("publication replay repeated side effect")
	}
	select {
	case <-index.wake:
	default:
		t.Fatal("publication did not schedule index refresh")
	}
}

func TestFailedPublicationReusesPreparedCommit(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	id := publishFixture(t, db)
	repo := &publishingRepository{publishErr: errors.New("push failed")}
	publisher := NewWikiService(db, repo)
	if _, err := publisher.ApplyChangeset(context.Background(), id); err == nil {
		t.Fatal("push failure ignored")
	}
	details, err := publisher.GetChangeset(context.Background(), id)
	if err != nil || details.Changeset.ResultCommitSHA == "" || details.Changeset.PublicationOwner != "" {
		t.Fatal("prepared commit or lease recovery lost")
	}
	repo.publishErr = nil
	result, err := publisher.ApplyChangeset(context.Background(), id)
	if err != nil || result.Changeset.Status != model.ChangesetStatusCommitted || repo.prepared != 1 || repo.published != 2 {
		t.Fatal("retry did not reuse prepared commit")
	}
}
