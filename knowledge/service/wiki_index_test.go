package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/database"
	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testSnapshotSource struct {
	snapshot repository.Snapshot
	err      error
	calls    int
}

func (s *testSnapshotSource) Snapshot(context.Context) (repository.Snapshot, error) {
	s.calls++
	return s.snapshot, s.err
}
func indexFixture(t *testing.T) (*gorm.DB, *WikiIndex, *WikiService, *testSnapshotSource) {
	t.Helper()
	port := os.Getenv("BLIX_TEST_DATABASE_PORT")
	if port == "" {
		t.Skip("BLIX_TEST_DATABASE_PORT is required for PostgreSQL integration tests")
	}
	db, err := database.Open(context.Background(), config.Config{DatabaseHost: "127.0.0.1", DatabasePort: port, DatabaseUser: "postgres", DatabasePassword: "blix-test", DatabaseName: "blix_test"})
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	if err := db.Exec("TRUNCATE knowledge_wiki_index_state, knowledge_wiki_page, knowledge_wiki_section").Error; err != nil {
		t.Fatal(err)
	}
	source := &testSnapshotSource{snapshot: repository.Snapshot{CommitSHA: strings.Repeat("a", 40), Pages: []repository.Page{
		{Path: "seasons/gr26/brakes.md", BlobSHA: strings.Repeat("1", 40), Content: "# GR26 braking\n\n## Hydraulic configuration\nThe brakes use a dual master cylinder system.\n\n## Testing\nBrake pressure testing remains pending.\n"},
		{Path: "seasons/gr27/brakes.md", BlobSHA: strings.Repeat("2", 40), Content: "# GR27 braking\nThe brakes use revised hydraulic routing.\n"},
	}}}
	index := NewWikiIndex(db, source)
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, index, NewWikiService(db, nil), source
}

func TestIndexedRetrievalEvaluation(t *testing.T) {
	_, _, service, source := indexFixture(t)
	for _, tc := range []struct{ query, season, want string }{
		{"What do we know about GR26?", "", "seasons/gr26/brakes.md"},
		{"GR26 brake configuration", "gr26", "seasons/gr26/brakes.md"},
		{"brakes", "gr27", "seasons/gr27/brakes.md"},
		{"hydraulic configuration", "gr26", "seasons/gr26/brakes.md"},
	} {
		t.Run(tc.query+tc.season, func(t *testing.T) {
			result, err := service.SearchIndexed(context.Background(), WikiSearchOptions{Query: tc.query, Season: tc.season, Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Results) == 0 || result.Results[0].Path != tc.want {
				t.Fatalf("hits=%+v", result.Results)
			}
			if result.CommitSHA != source.snapshot.CommitSHA || result.Stale {
				t.Fatalf("freshness=%+v", result.WikiFreshness)
			}
			for _, hit := range result.Results {
				if tc.season != "" && hit.Season != tc.season {
					t.Fatal("season filter leaked")
				}
			}
		})
	}
	for _, q := range []string{"unobtainium", "the and is", "' & ! | :*"} {
		result, err := service.SearchIndexed(context.Background(), WikiSearchOptions{Query: q, Limit: 8})
		if err != nil || len(result.Results) != 0 {
			t.Fatalf("no-answer query %q: %+v %v", q, result, err)
		}
	}
	if source.calls != 1 {
		t.Fatal("retrieval contacted GitHub")
	}
}

func TestRefreshFailurePreservesReadableSnapshot(t *testing.T) {
	_, index, service, source := indexFixture(t)
	source.err = errors.New("GitHub unavailable")
	if err := index.Refresh(context.Background()); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	result, err := service.ReadIndexedPage(context.Background(), "seasons/gr26/brakes.md", WikiReadOptions{})
	if err != nil || !result.Stale || result.RefreshError == "" || !strings.Contains(result.Page.Content, "master cylinder") {
		t.Fatalf("page=%+v err=%v", result, err)
	}
	source.err = nil
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err = service.ReadIndexedPage(context.Background(), "seasons/gr26/brakes.md", WikiReadOptions{})
	if err != nil || result.Stale {
		t.Fatal("successful refresh did not restore freshness")
	}
}

func TestIndexRefreshUpdatesDeletesAndRollsBack(t *testing.T) {
	_, index, service, source := indexFixture(t)
	source.snapshot.CommitSHA = strings.Repeat("b", 40)
	source.snapshot.Pages = []repository.Page{{Path: "team/test.md", BlobSHA: strings.Repeat("3", 40), Content: "# New design\nVerified pedal travel.\n"}}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadIndexedPage(context.Background(), "seasons/gr26/brakes.md", WikiReadOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed page still served")
	}
	source.snapshot.CommitSHA = strings.Repeat("c", 40)
	source.snapshot.Pages = []repository.Page{
		{Path: "team/test.md", BlobSHA: strings.Repeat("4", 40), Content: "# Changed\nIncorrect content"},
		{Path: "../bad.md", Content: "Bad"},
	}
	if err := index.Refresh(context.Background()); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	page, err := service.ReadIndexedPage(context.Background(), "team/test.md", WikiReadOptions{})
	if err != nil || page.CommitSHA != strings.Repeat("b", 40) || !strings.Contains(page.Page.Content, "Verified") {
		t.Fatalf("partial snapshot published: %+v %v", page, err)
	}
}

func TestBoundedReadsAndRevisionPinning(t *testing.T) {
	_, index, service, source := indexFixture(t)
	original := "# Unicode\n" + strings.Repeat("车辆 braking pressure\n", 1500)
	source.snapshot.CommitSHA = strings.Repeat("b", 40)
	source.snapshot.Pages = []repository.Page{{Path: "team/large.md", BlobSHA: strings.Repeat("3", 40), Content: original}}
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	options := WikiReadOptions{MaxBytes: 8000, CommitSHA: source.snapshot.CommitSHA}
	assembled := ""
	for {
		page, err := service.ReadIndexedPage(context.Background(), "team/large.md", options)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Page.Content) > 8000 {
			t.Fatal("read exceeded budget")
		}
		assembled += page.Page.Content
		if !page.Truncated {
			break
		}
		if page.NextCursor == nil || *page.NextCursor <= options.Cursor {
			t.Fatal("cursor did not advance")
		}
		options.Cursor = *page.NextCursor
	}
	if assembled != original {
		t.Fatal("cursor dropped or duplicated content")
	}
	section := 1
	page, err := service.ReadIndexedPage(context.Background(), "team/large.md", WikiReadOptions{Section: &section})
	if err != nil || len(page.Page.Content) == 0 || len(page.Outline) < 2 {
		t.Fatal("section read failed")
	}
	if _, err := service.ReadIndexedPage(context.Background(), "team/large.md", WikiReadOptions{CommitSHA: strings.Repeat("a", 40)}); !errors.Is(err, ErrConflict) {
		t.Fatal("mismatched revision accepted")
	}
	result, err := service.SearchIndexed(context.Background(), WikiSearchOptions{Query: "pressure", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, hit := range result.Results {
		total += len(hit.Content)
	}
	if total > wikiSearchBudget {
		t.Fatal("search exceeded total context budget")
	}
}

func TestConcurrentReadersNeverObserveMixedRevisions(t *testing.T) {
	_, index, service, source := indexFixture(t)
	var readers sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		readers.Go(func() {
			for range 12 {
				page, err := service.ReadIndexedPage(context.Background(), "seasons/gr26/brakes.md", WikiReadOptions{})
				if err != nil {
					errorsFound <- err
					return
				}
				if page.CommitSHA == strings.Repeat("a", 40) && !strings.Contains(page.Page.Content, "master cylinder") || page.CommitSHA == strings.Repeat("b", 40) && page.Page.Content != "# Updated\nNew brakes" {
					errorsFound <- errors.New("mixed snapshot")
					return
				}
			}
		})
	}
	source.snapshot.CommitSHA = strings.Repeat("b", 40)
	source.snapshot.Pages[0].BlobSHA = strings.Repeat("4", 40)
	source.snapshot.Pages[0].Content = "# Updated\nNew brakes"
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatal("reader triggered refresh")
	}
}

func TestMissingEvidenceIsExplicit(t *testing.T) {
	_, index, service, source := indexFixture(t)
	id := "sourcerecord_01m3x1y8x6rw60js5gn11az2xe"
	source.snapshot.CommitSHA = strings.Repeat("b", 40)
	source.snapshot.Pages[0].BlobSHA = strings.Repeat("4", 40)
	source.snapshot.Pages[0].Content = "# Evidence\nBraking claim [source](" + id + ")."
	if err := index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := service.ReadIndexedPage(context.Background(), source.snapshot.Pages[0].Path, WikiReadOptions{})
	if err != nil || len(page.MissingCitations) != 1 || page.MissingCitations[0] != id {
		t.Fatalf("missing evidence hidden: %+v %v", page, err)
	}
	var state model.WikiIndexState
	if err := index.db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
}
