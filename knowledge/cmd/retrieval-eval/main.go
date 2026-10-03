package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/config"
	"github.com/gaucho-racing/blix/knowledge/database"
	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/embedding"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/service"
	"gorm.io/gorm/logger"
)

type document struct {
	Key      string `json:"key"`
	Filename string `json:"filename"`
	Title    string `json:"title"`
	Hash     string `json:"sha256"`
}
type question struct {
	ID       string   `json:"id"`
	Document string   `json:"document"`
	Category string   `json:"category"`
	Query    string   `json:"query"`
	Pages    []int    `json:"expected_pages"`
	Patterns []string `json:"answer_patterns"`
	Image    bool     `json:"requires_image"`
	Abstain  bool     `json:"expect_abstention"`
}
type suite struct {
	Version   int        `json:"version"`
	Documents []document `json:"documents"`
	Questions []question `json:"questions"`
}
type visual struct {
	Document    string `json:"document"`
	Page        int    `json:"page"`
	Description string `json:"description"`
	Model       string `json:"model"`
}
type measurement struct {
	question
	Mode           string                       `json:"mode"`
	LatencyMS      int64                        `json:"latency_ms"`
	HitAt1         bool                         `json:"hit_at_1"`
	HitAt5         bool                         `json:"hit_at_5"`
	ReciprocalRank float64                      `json:"reciprocal_rank"`
	Retrieval      service.DocumentSearchResult `json:"retrieval"`
}
type extractor struct{}

func (extractor) Extract(ctx context.Context, path string) ([]string, error) {
	data, err := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", path, "-").Output()
	if err != nil {
		return nil, fmt.Errorf("extract PDF: %w", err)
	}
	pages := strings.Split(string(data), "\f")
	if pages[len(pages)-1] == "" {
		pages = pages[:len(pages)-1]
	}
	return pages, nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	suitePath := flag.String("suite", "evals/retrieval.json", "Question suite JSON")
	corpus := flag.String("corpus", "", "Directory containing the PDFs")
	port := flag.String("port", "15433", "Isolated evaluation PostgreSQL port")
	databaseName := flag.String("database", "blix_retrieval_eval", "Isolated database name ending in _eval")
	mode := flag.String("mode", "all", "lexical, hybrid, or all")
	visualsPath := flag.String("visuals", "", "Optional model-generated visual descriptions JSON")
	flag.Parse()
	if !strings.HasSuffix(*databaseName, "_eval") || strings.ContainsAny(*databaseName, " \x00'\";\\") {
		return fmt.Errorf("evaluation requires an isolated database name ending in _eval")
	}
	if *mode != "all" && *mode != "lexical" && *mode != "hybrid" {
		return fmt.Errorf("invalid mode")
	}
	if *corpus == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*corpus = filepath.Join(home, "Downloads")
	}
	data, err := os.ReadFile(*suitePath)
	if err != nil {
		return err
	}
	var cases suite
	if err = json.Unmarshal(data, &cases); err != nil {
		return err
	}
	if cases.Version != 1 || len(cases.Questions) == 0 {
		return fmt.Errorf("invalid evaluation suite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.DatabaseHost = "127.0.0.1"
	cfg.DatabasePort = *port
	cfg.DatabaseName = *databaseName
	cfg.DatabaseUser = "postgres"
	cfg.DatabasePassword = "blix-eval"
	db, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	directory, err := os.MkdirTemp("", "blix-retrieval-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	documents := service.NewDocumentService(db, directory, extractor{})
	sources := service.NewSourceService(db)
	ids := map[string]string{}
	var allIDs []string
	stored := map[string]model.Document{}
	for _, doc := range cases.Documents {
		path := filepath.Join(*corpus, doc.Filename)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != doc.Hash {
			return fmt.Errorf("PDF version mismatch: %s", doc.Filename)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		imported, err := documents.Import(ctx, file, service.DocumentMetadata{Title: doc.Title, Filename: doc.Filename, Publisher: "Evaluation fixture", Version: "1", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UploadedBy: identifier.New(identifier.Entity)})
		file.Close()
		if err != nil {
			return err
		}
		if err := db.Model(&imported.Document).Update("review_status", model.DocumentReviewIngested).Error; err != nil {
			return err
		}
		ids[doc.Key] = imported.Document.ID
		allIDs = append(allIDs, imported.Document.ID)
		stored[doc.Key] = imported.Document
	}
	if *visualsPath != "" {
		data, err := os.ReadFile(*visualsPath)
		if err != nil {
			return err
		}
		var visuals []visual
		if err = json.Unmarshal(data, &visuals); err != nil {
			return err
		}
		for _, v := range visuals {
			doc, ok := stored[v.Document]
			if !ok || v.Page < 1 || v.Page > doc.PageCount || v.Model == "" {
				return fmt.Errorf("invalid visual fixture")
			}
			record, err := sources.IngestSourceRecord(ctx, doc.SourceID, service.IngestSourceRecordInput{ExternalID: fmt.Sprintf("page:%d:visual", v.Page), ExternalVersion: fmt.Sprintf("%x", sha256.Sum256([]byte(v.Description))), ParentExternalID: fmt.Sprintf("page:%d", v.Page), RecordType: "pdf_page_visual", Content: v.Description, EvidenceType: "agent_summary", CaptureMethod: "document_vision", OccurredAt: time.Now().UTC()})
			if err != nil {
				return err
			}
			if err := db.Model(&model.DocumentPage{}).Where("document_id=? AND number=?", doc.ID, v.Page).Update("visual_source_record_id", record.Record.ID).Error; err != nil {
				return err
			}
		}
	}
	for {
		n, err := sources.IndexPendingRecords(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	var embeddings *service.EmbeddingService
	if cfg.EmbeddingAPIKey != "" && *mode != "lexical" {
		client := embedding.New(cfg.EmbeddingEndpoint, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
		client.SendDimensions = cfg.EmbeddingSendDimensions
		namespace := client.Namespace(cfg.EmbeddingRevision)
		embeddings = service.NewEmbeddingService(db, client, namespace, cfg.EmbeddingDimensions)
		documents.SetEmbeddings(embeddings)
		for {
			if err := embeddings.Seed(ctx); err != nil {
				return err
			}
			n, err := embeddings.ProcessBatch(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
	}
	if *mode == "hybrid" && embeddings == nil {
		return fmt.Errorf("hybrid evaluation requires EMBEDDING_ENDPOINT, EMBEDDING_API_KEY and EMBEDDING_MODEL")
	}
	modes := []string{"lexical"}
	if *mode == "hybrid" {
		modes = []string{"hybrid"}
	} else if *mode == "all" && embeddings != nil {
		modes = append(modes, "hybrid")
	}
	results := []measurement{}
	for _, mode := range modes {
		for _, q := range cases.Questions {
			if ids[q.Document] == "" {
				return fmt.Errorf("unknown question document")
			}
			started := time.Now()
			result, err := documents.SearchSources(ctx, service.DocumentSearchOptions{Query: q.Query, Limit: 20, Mode: mode}, service.DocumentRetrievalScope{DocumentIDs: allIDs})
			if err != nil {
				return err
			}
			m := measurement{question: q, Mode: mode, LatencyMS: time.Since(started).Milliseconds(), Retrieval: result}
			for rank, hit := range result.Results {
				if hit.DocumentID == ids[q.Document] && slices.Contains(q.Pages, hit.PDFPage) {
					m.ReciprocalRank = 1 / float64(rank+1)
					m.HitAt1 = rank == 0
					m.HitAt5 = rank < 5
					break
				}
			}
			results = append(results, m)
		}
	}
	report := map[string]any{"suite_version": cases.Version, "embedding_model": cfg.EmbeddingModel, "embedding_dimensions": cfg.EmbeddingDimensions, "hybrid_evaluated": embeddings != nil, "documents": ids, "results": results}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
