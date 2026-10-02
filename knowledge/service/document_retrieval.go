package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
)

type DocumentRetrievalScope struct {
	DocumentIDs     []string `json:"document_ids"`
	Team            bool     `json:"-"`
	AllowUnapproved bool     `json:"-"`
}

type DocumentSearchOptions struct {
	Query      string `json:"query"`
	DocumentID string `json:"document_id,omitempty"`
	Limit      int    `json:"limit"`
}

type DocumentSearchContext struct {
	DocumentID        string `json:"document_id"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	ReviewStatus      string `json:"review_status"`
	PageCount         int    `json:"page_count"`
	IndexedPages      int    `json:"indexed_pages"`
	ReadyForQuestions bool   `json:"ready_for_questions"`
}

type DocumentPassage struct {
	EvidenceType    string  `json:"evidence_type"`
	CaptureMethod   string  `json:"capture_method"`
	ChunkID         string  `json:"source_chunk_id"`
	SourceRecordID  string  `json:"source_record_id"`
	DocumentID      string  `json:"document_id"`
	Title           string  `json:"title"`
	PDFPage         int     `json:"pdf_page"`
	Ordinal         int     `json:"ordinal"`
	Content         string  `json:"content"`
	StartOffset     int     `json:"start_offset"`
	EndOffset       int     `json:"end_offset"`
	OffsetUnit      string  `json:"offset_unit"`
	Score           float64 `json:"score"`
	MatchedAllTerms bool    `json:"matched_all_terms"`
	Truncated       bool    `json:"truncated"`
	CitationURL     string  `json:"citation_url"`
	ReviewStatus    string  `json:"review_status"`
}

type DocumentSearchResult struct {
	Retrieval string                  `json:"retrieval"`
	Documents []DocumentSearchContext `json:"documents"`
	Results   []DocumentPassage       `json:"results"`
	Truncated bool                    `json:"truncated"`
}

func validateRetrievalIDs(ids []string) error {
	if len(ids) > 8 {
		return ErrInvalid
	}
	for i, id := range ids {
		if !identifier.IsValid(id, identifier.Document) || slices.Contains(ids[:i], id) {
			return ErrInvalid
		}
	}
	return nil
}

func (s *DocumentService) ValidateRetrievalDocuments(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return ErrInvalid
	}
	if err := validateRetrievalIDs(ids); err != nil {
		return err
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Document{}).Where("id IN ? AND review_status = ? AND size_bytes > 0", ids, model.DocumentReviewIngested).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrNotFound
	}
	return nil
}

func documentRetrievalQuery(db *gorm.DB, scope DocumentRetrievalScope, id string) (*gorm.DB, error) {
	if err := validateRetrievalIDs(scope.DocumentIDs); err != nil {
		return nil, err
	}
	if id != "" && !identifier.IsValid(id, identifier.Document) {
		return nil, ErrInvalid
	}
	query := db.Table("knowledge_document d").Joins("LEFT JOIN knowledge_source s ON s.id = d.source_id").Where("(d.source_id = '' OR s.status = ?)", model.SourceStatusActive).Where("d.review_status <> ?", model.DocumentReviewRejected)
	if !scope.AllowUnapproved || scope.Team {
		query = query.Where("d.review_status = ?", model.DocumentReviewIngested)
	}
	if !scope.Team {
		if id != "" && !slices.Contains(scope.DocumentIDs, id) {
			return nil, ErrForbidden
		}
		if len(scope.DocumentIDs) == 0 {
			query = query.Where("false")
		} else {
			query = query.Where("d.id IN ?", scope.DocumentIDs)
		}
	}
	if id != "" {
		query = query.Where("d.id = ?", id)
	}
	return query, nil
}

func retrievalDocuments(db *gorm.DB, scope DocumentRetrievalScope, id string) ([]DocumentSearchContext, error) {
	query, err := documentRetrievalQuery(db, scope, id)
	if err != nil {
		return nil, err
	}
	documents := []DocumentSearchContext{}
	err = query.Select(`d.id AS document_id, d.title, d.status, d.review_status, d.page_count,
      (SELECT count(*) FROM knowledge_document_page p JOIN knowledge_source_record_index i ON i.source_record_id = p.source_record_id AND i.chunker_version = ? WHERE p.document_id = d.id) AS indexed_pages`, sourceChunkerVersion).Order("d.created_at DESC, d.id DESC").Limit(100).Scan(&documents).Error
	for i := range documents {
		documents[i].ReadyForQuestions = documents[i].PageCount > 0 && documents[i].IndexedPages == documents[i].PageCount
	}
	return documents, err
}

func (s *DocumentService) ListRetrievableDocuments(ctx context.Context, scope DocumentRetrievalScope) ([]DocumentSearchContext, error) {
	return retrievalDocuments(s.db.WithContext(ctx), scope, "")
}

func (s *DocumentService) SearchSources(ctx context.Context, options DocumentSearchOptions, scope DocumentRetrievalScope) (DocumentSearchResult, error) {
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" || len(options.Query) > 256 || !utf8.ValidString(options.Query) || strings.ContainsRune(options.Query, 0) || options.Limit < 1 || options.Limit > 20 {
		return DocumentSearchResult{}, ErrInvalid
	}
	result := DocumentSearchResult{Retrieval: "document_lexical_v1", Documents: []DocumentSearchContext{}, Results: []DocumentPassage{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result.Documents, err = retrievalDocuments(tx, scope, options.DocumentID)
		if err != nil {
			return err
		}
		query, err := documentRetrievalQuery(tx, scope, options.DocumentID)
		if err != nil {
			return err
		}
		rules := []string{}
		for _, rule := range ruleIdentifier.FindAllString(options.Query, -1) {
			rule = strings.ToUpper(rule)
			if !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
		rulesJSON, err := json.Marshal(rules)
		if err != nil {
			return err
		}
		query = query.Joins("JOIN knowledge_document_page p ON p.document_id = d.id").Joins("JOIN knowledge_source_chunk c ON (c.source_record_id = p.source_record_id OR c.source_record_id = p.visual_source_record_id) AND c.chunker_version = ?", sourceChunkerVersion).
			Joins("JOIN knowledge_source_record r ON r.id = c.source_record_id").Joins(`CROSS JOIN (SELECT plainto_tsquery('english', ?) AS all_terms, replace(plainto_tsquery('english', ?)::text, ' & ', ' | ')::tsquery AS any_terms) q`, options.Query, options.Query).
			Where("c.search_vector @@ q.any_terms").Where("c.rule_ids @> ?::jsonb", string(rulesJSON))
		err = query.Select(`r.evidence_type, r.capture_method, c.id AS chunk_id, c.source_record_id, d.id AS document_id, d.title, p.number AS pdf_page, c.ordinal, c.content, c.start_offset, c.end_offset,
          d.review_status, c.search_vector @@ q.all_terms AS matched_all_terms, ts_rank_cd(c.search_vector, q.any_terms, 32) AS score`).
			Order("matched_all_terms DESC, score DESC, d.id ASC, p.number ASC, c.ordinal ASC").Limit(options.Limit + 1).Scan(&result.Results).Error
		if err != nil {
			return err
		}
		if len(result.Results) > options.Limit {
			result.Results = result.Results[:options.Limit]
			result.Truncated = true
		}
		remaining := 16000
		for i := range result.Results {
			if remaining == 0 {
				result.Results = result.Results[:i]
				result.Truncated = true
				break
			}
			hit := &result.Results[i]
			hit.OffsetUnit = "utf8_bytes"
			hit.CitationURL = fmt.Sprintf("/knowledge/documents/%s?preview=1&page=%d", hit.DocumentID, hit.PDFPage)
			if len(hit.Content) > remaining {
				end := remaining
				for end > 0 && !utf8.RuneStart(hit.Content[end]) {
					end--
				}
				hit.Content = hit.Content[:end]
				hit.EndOffset = hit.StartOffset + end
				hit.Truncated = true
				result.Results = result.Results[:i+1]
				result.Truncated = true
				break
			}
			remaining -= len(hit.Content)
		}
		return nil
	})
	return result, err
}

type DocumentEvidencePage struct {
	DocumentID     string `json:"document_id"`
	Title          string `json:"title"`
	PDFPage        int    `json:"pdf_page"`
	SourceRecordID string `json:"source_record_id"`
	ReviewStatus   string `json:"review_status"`
	Content        string `json:"content"`
	Cursor         int    `json:"cursor"`
	NextCursor     *int   `json:"next_cursor,omitempty"`
	TotalBytes     int    `json:"total_bytes"`
	CitationURL    string `json:"citation_url"`
}

func (s *DocumentService) ReadEvidencePage(ctx context.Context, id string, page, cursor int, scope DocumentRetrievalScope) (DocumentEvidencePage, error) {
	if page < 1 || cursor < 0 {
		return DocumentEvidencePage{}, ErrInvalid
	}
	result := DocumentEvidencePage{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query, err := documentRetrievalQuery(tx, scope, id)
		if err != nil {
			return err
		}
		row := query.Joins("JOIN knowledge_document_page p ON p.document_id = d.id AND p.number = ?", page).Joins("JOIN knowledge_source_record r ON r.id = p.source_record_id").
			Select("d.id AS document_id, d.title, d.review_status, p.number AS pdf_page, r.id AS source_record_id, r.content").Scan(&result)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected == 0 {
			return ErrNotFound
		}
		result.TotalBytes = len(result.Content)
		if cursor > result.TotalBytes || cursor < result.TotalBytes && !utf8.RuneStart(result.Content[cursor]) {
			return ErrInvalid
		}
		end := min(cursor+8000, result.TotalBytes)
		for end < result.TotalBytes && !utf8.RuneStart(result.Content[end]) {
			end--
		}
		if end < result.TotalBytes {
			result.NextCursor = &end
		}
		result.Content = result.Content[cursor:end]
		result.Cursor = cursor
		result.CitationURL = fmt.Sprintf("/knowledge/documents/%s?preview=1&page=%d", id, page)
		return nil
	})
	return result, err
}
