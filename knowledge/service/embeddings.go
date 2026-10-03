package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type EmbeddingService struct {
	db         *gorm.DB
	provider   Embedder
	namespace  string
	dimensions int
	queries    chan struct{}
	cacheMu    sync.Mutex
	cache      map[string]queryEmbedding
}
type queryEmbedding struct {
	vector  pgvector.Vector
	expires time.Time
}

func NewEmbeddingService(db *gorm.DB, provider Embedder, namespace string, dimensions int) *EmbeddingService {
	return &EmbeddingService{db: db, provider: provider, namespace: namespace, dimensions: dimensions, queries: make(chan struct{}, 8), cache: map[string]queryEmbedding{}}
}

func wikiEmbeddingText(title, heading, content string) string {
	return title + "\n" + heading + "\n" + content
}

func (s *EmbeddingService) Query(ctx context.Context, query string) (pgvector.Vector, string) {
	if s == nil || s.provider == nil {
		return pgvector.Vector{}, "disabled"
	}
	key := hashBytes([]byte(query))
	s.cacheMu.Lock()
	cached, ok := s.cache[key]
	s.cacheMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.vector, "ready"
	}
	select {
	case s.queries <- struct{}{}:
		defer func() { <-s.queries }()
	default:
		return pgvector.Vector{}, "busy"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	vectors, err := s.provider.Embed(queryCtx, []string{query})
	if err != nil || len(vectors) != 1 || len(vectors[0]) != s.dimensions {
		return pgvector.Vector{}, "unavailable"
	}
	vector := pgvector.NewVector(vectors[0])
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.cache) >= 256 {
		clear(s.cache)
	}
	s.cache[key] = queryEmbedding{vector: vector, expires: time.Now().Add(5 * time.Minute)}
	return vector, "ready"
}

func (s *EmbeddingService) Seed(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(`INSERT INTO knowledge_embedding
 (namespace,content_hash,content,status,attempts,available_at,lease_token,last_error,updated_at)
 SELECT DISTINCT ?, content_hash, content, 'pending',0,now(),'','',now() FROM (
 SELECT c.content_hash,c.content FROM knowledge_source_chunk c
 WHERE c.chunker_version = ? AND EXISTS (SELECT 1 FROM knowledge_document_page p JOIN knowledge_document d ON d.id=p.document_id
 WHERE (p.source_record_id=c.source_record_id OR p.visual_source_record_id=c.source_record_id) AND d.review_status <> ?)
 UNION ALL
 SELECT section.content_hash, page.title||E'\n'||section.heading||E'\n'||section.content
 FROM knowledge_wiki_section section JOIN knowledge_wiki_page page USING(path) WHERE section.content_hash <> ''
 ) inputs WHERE NOT EXISTS (SELECT 1 FROM knowledge_embedding e WHERE e.namespace=? AND e.content_hash=inputs.content_hash)
 ORDER BY content_hash LIMIT 256 ON CONFLICT DO NOTHING`, s.namespace, sourceChunkerVersion, model.DocumentReviewRejected, s.namespace).Error
}

func (s *EmbeddingService) ProcessBatch(ctx context.Context) (int, error) {
	var jobs []model.Embedding
	token := identifier.New(identifier.Session)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("namespace=? AND ((status IN ? AND available_at<=now()) OR (status='processing' AND lease_until<now()))", s.namespace, []string{"pending", "retry"}).Order("available_at, content_hash").Limit(32).Find(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		hashes := make([]string, len(jobs))
		for j := range jobs {
			jobs[j].Attempts++
			hashes[j] = jobs[j].ContentHash
		}
		return tx.Model(&model.Embedding{}).Where("namespace=? AND content_hash IN ?", s.namespace, hashes).Updates(map[string]any{
			"status": "processing", "lease_token": token, "lease_until": time.Now().Add(time.Minute), "attempts": gorm.Expr("attempts+1"),
		}).Error
	})
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	texts := make([]string, len(jobs))
	for j := range jobs {
		texts[j] = jobs[j].Content
	}
	batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	vectors, embedErr := s.provider.Embed(batchCtx, texts)
	cancel()
	if embedErr == nil && len(vectors) != len(jobs) {
		embedErr = fmt.Errorf("embedding response count mismatch")
	}
	if embedErr == nil {
		for _, v := range vectors {
			if len(v) != s.dimensions {
				embedErr = fmt.Errorf("embedding response dimensions mismatch")
				break
			}
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	err = s.db.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		for j, job := range jobs {
			updates := map[string]any{"lease_token": "", "lease_until": nil, "last_error": "", "status": "ready"}
			if embedErr != nil {
				updates["status"] = "retry"
				updates["last_error"] = "Embedding provider unavailable; keyword search remains available."
				updates["available_at"] = time.Now().Add(time.Duration(1<<min(job.Attempts, 5)) * 30 * time.Second)
			} else {
				v := pgvector.NewVector(vectors[j])
				updates["vector"] = v
			}
			if err := tx.Model(&model.Embedding{}).Where("namespace=? AND content_hash=? AND lease_token=?", s.namespace, job.ContentHash, token).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(jobs), embedErr
}

func (s *EmbeddingService) Run(ctx context.Context, report func(error)) {
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for ctx.Err() == nil {
				n, err := s.ProcessBatch(ctx)
				if err != nil && ctx.Err() == nil {
					report(err)
				}
				if n == 32 && err == nil {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		})
	}
	defer workers.Wait()
	for ctx.Err() == nil {
		if err := s.Seed(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func (s *DocumentService) SetEmbeddings(embeddings *EmbeddingService) { s.embeddings = embeddings }
func (s *WikiService) SetEmbeddings(embeddings *EmbeddingService)     { s.embeddings = embeddings }
