package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

type fixtureEmbeddings struct {
	fail  bool
	calls int
}

func (p *fixtureEmbeddings) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	if p.fail {
		return nil, errors.New("provider down")
	}
	vectors := make([][]float32, len(inputs))
	for j, text := range inputs {
		vectors[j] = []float32{1, 0}
		if strings.Contains(text, "unrelated") {
			vectors[j] = []float32{0, 1}
		}
	}
	return vectors, nil
}

func TestHybridDocumentScopeAndExactRules(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	docs := NewDocumentService(db, t.TempDir(), &fixtureExtractor{pages: []string{"T.3.2 hydraulic safety mechanism", "T.3.20 unrelated electronics"}})
	first, err := docs.Import(ctx, bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	second, err := docs.Import(ctx, bytes.NewBufferString("%PDF-"+identifier.New(identifier.Document)), documentMetadata())
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&second.Document).Update("review_status", model.DocumentReviewIngested)
	provider := &fixtureEmbeddings{}
	namespace := identifier.New(identifier.Session)
	embeddings := NewEmbeddingService(db, provider, namespace, 2)
	docs.SetEmbeddings(embeddings)
	if err := embeddings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := embeddings.ProcessBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	search := func(query string, scope DocumentRetrievalScope) DocumentSearchResult {
		t.Helper()
		r, err := docs.SearchSources(ctx, DocumentSearchOptions{Query: query, Limit: 10}, scope)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	scope := DocumentRetrievalScope{DocumentIDs: []string{first.Document.ID}, AllowUnapproved: true}
	result := search("braking", scope)
	if result.Retrieval != "document_hybrid_v1" || len(result.Results) != 2 || result.Results[0].PDFPage != 1 {
		t.Fatalf("semantic ranking: %+v", result)
	}
	for _, h := range result.Results {
		if h.DocumentID != first.Document.ID || h.SourceRecordID == "" || h.CitationURL == "" {
			t.Fatal("scope or citation lost")
		}
	}
	for _, h := range search("braking", DocumentRetrievalScope{Team: true, AllowUnapproved: true}).Results {
		if h.DocumentID == first.Document.ID {
			t.Fatal("pending document leaked into team retrieval")
		}
	}
	exact := search("T.3.2", scope)
	if len(exact.Results) != 1 || exact.Results[0].PDFPage != 1 {
		t.Fatalf("exact identifier became a semantic suggestion: %+v", exact)
	}
	db.Model(&first.Document).Update("review_status", model.DocumentReviewRejected)
	if len(search("braking", scope).Results) != 0 {
		t.Fatal("rejected embeddings remained usable")
	}
	db.Model(&second.Document).Update("review_status", model.DocumentReviewIngested)
	provider.fail = true
	embeddings.cache = map[string]queryEmbedding{}
	fallback := search("hydraulic", DocumentRetrievalScope{Team: true})
	if len(fallback.Results) == 0 || fallback.SemanticStatus != "unavailable" || fallback.Retrieval != "document_lexical_v1" {
		t.Fatalf("keyword fallback lost: %+v", fallback)
	}
}

func TestEmbeddingRetryFencingAndCacheReuse(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	provider := &fixtureEmbeddings{fail: true}
	namespace := identifier.New(identifier.Session)
	s := NewEmbeddingService(db, provider, namespace, 2)
	hash := hashBytes([]byte("shared text"))
	job := model.Embedding{Namespace: namespace, ContentHash: hash, Content: "shared text", Status: "pending", AvailableAt: time.Now()}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := s.ProcessBatch(ctx); n != 1 || err == nil {
		t.Fatal("provider failure wasn't retried")
	}
	db.First(&job, "namespace=? AND content_hash=?", namespace, hash)
	if job.Status != "retry" || job.Vector != nil || job.LeaseToken != "" || job.Attempts != 1 {
		t.Fatalf("failed job: %+v", job)
	}
	if n, err := s.ProcessBatch(ctx); n != 0 || err != nil {
		t.Fatal("retry ignored backoff")
	}
	db.Model(&job).Update("available_at", time.Now().Add(-time.Second))
	provider.fail = false
	if _, err := s.ProcessBatch(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&job, "namespace=? AND content_hash=?", namespace, hash)
	if job.Status != "ready" || job.Vector == nil || job.Attempts != 2 {
		t.Fatalf("successful retry: %+v", job)
	}
	if n, err := s.ProcessBatch(ctx); n != 0 || err != nil {
		t.Fatal("ready content was embedded again")
	}
	calls := provider.calls
	s.Query(ctx, "cached question")
	s.Query(ctx, "cached question")
	if provider.calls != calls+1 {
		t.Fatal("query embeddings weren't cached")
	}
	stale := model.Embedding{Namespace: namespace, ContentHash: hashBytes([]byte("stale text")), Content: "stale text", Status: "processing", LeaseToken: "old", LeaseUntil: new(time.Now().Add(-time.Minute)), AvailableAt: time.Now()}
	db.Create(&stale)
	if n, err := s.ProcessBatch(ctx); n != 1 || err != nil {
		t.Fatalf("expired lease wasn't recovered: %d %v", n, err)
	}
	if r := db.Model(&stale).Where("lease_token='old'").Update("status", "retry"); r.RowsAffected != 0 {
		t.Fatal("stale worker overwrote completion")
	}
}

func TestWikiSemanticRefreshNeverReturnsStaleSection(t *testing.T) {
	db, index, wiki, repo := indexFixture(t)
	ctx := context.Background()
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	provider := &fixtureEmbeddings{}
	s := NewEmbeddingService(db, provider, identifier.New(identifier.Session), 2)
	wiki.SetEmbeddings(s)
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := s.ProcessBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	result, err := wiki.SearchIndexed(ctx, WikiSearchOptions{Query: "semantic paraphrase", Limit: 8})
	if err != nil || result.Retrieval != "hybrid_v1" || len(result.Results) == 0 {
		t.Fatalf("wiki semantic search: %+v %v", result, err)
	}
	repo.snapshot.Pages = nil
	repo.snapshot.CommitSHA = strings.Repeat("b", 40)
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = wiki.SearchIndexed(ctx, WikiSearchOptions{Query: "semantic paraphrase", Limit: 8})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("removed wiki content survived in embeddings: %+v %v", result, err)
	}
}

func TestRankFusionRewardsBothBranchesAndKeepsLexicalTieOrder(t *testing.T) {
	got := fuseRanks([]string{"exact", "shared"}, []string{"semantic", "shared"}, func(s string) string { return s }, func(_ *string, _ float64) {}, 3)
	if fmt.Sprint(got) != "[shared exact semantic]" {
		t.Fatal(got)
	}
}

type blockingEmbeddings struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingEmbeddings) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	close(p.started)
	select {
	case <-p.release:
		return [][]float32{{1, 0}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestConcurrentWorkersDoNotEmbedTheSameJob(t *testing.T) {
	db, _, _, _ := indexFixture(t)
	ctx := context.Background()
	namespace := identifier.New(identifier.Session)
	provider := &blockingEmbeddings{started: make(chan struct{}), release: make(chan struct{})}
	s := NewEmbeddingService(db, provider, namespace, 2)
	job := model.Embedding{Namespace: namespace, ContentHash: hashBytes([]byte("concurrent job")), Content: "concurrent job", Status: "pending", AvailableAt: time.Now()}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := s.ProcessBatch(ctx); finished <- err }()
	<-provider.started
	n, err := s.ProcessBatch(ctx)
	close(provider.release)
	if n != 0 || err != nil {
		t.Fatalf("second worker claimed an active lease: %d %v", n, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
