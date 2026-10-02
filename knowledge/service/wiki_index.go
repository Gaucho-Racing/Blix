package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/logger"
	"github.com/gaucho-racing/blix/knowledge/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const wikiIndexVersion = 1
const wikiRefreshInterval = 30 * time.Second
const wikiReadLimit = 8000
const wikiSearchBudget = 16000

type snapshotRepository interface {
	Snapshot(context.Context) (repository.Snapshot, error)
}

type WikiIndex struct {
	db      *gorm.DB
	source  snapshotRepository
	refresh sync.Mutex
	wake    chan struct{}
}

type WikiFreshness struct {
	CommitSHA    string    `json:"commit_sha"`
	IndexedAt    time.Time `json:"indexed_at"`
	CheckedAt    time.Time `json:"checked_at"`
	Stale        bool      `json:"stale"`
	RefreshError string    `json:"refresh_error,omitempty"`
}

type WikiOutline struct {
	Position int    `json:"position"`
	Heading  string `json:"heading"`
	Bytes    int    `json:"bytes"`
}

type WikiReadOptions struct {
	CommitSHA string `json:"commit_sha,omitempty"`
	Section   *int   `json:"section,omitempty"`
	Cursor    int    `json:"cursor,omitempty"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
}

type WikiIndexedPage struct {
	PageSourceRecordIDs    []string `json:"page_source_record_ids"`
	PageCitationsTruncated bool     `json:"page_citations_truncated"`
	WikiFreshness
	Page             repository.Page     `json:"page"`
	Title            string              `json:"title"`
	Season           string              `json:"season,omitempty"`
	OutlineTruncated bool                `json:"outline_truncated"`
	TotalSections    int                 `json:"total_sections"`
	Outline          []WikiOutline       `json:"outline"`
	Cursor           int                 `json:"cursor"`
	NextCursor       *int                `json:"next_cursor,omitempty"`
	Truncated        bool                `json:"truncated"`
	TotalBytes       int                 `json:"total_bytes"`
	Citations        map[string]Citation `json:"citations"`
	MissingCitations []string            `json:"missing_citations"`
}

type WikiSearchOptions struct {
	budget int
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
	Season string `json:"season,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

type WikiSectionHit struct {
	MatchedAllTerms        bool     `json:"matched_all_terms"`
	PageSourceRecordIDs    []string `json:"page_source_record_ids" gorm:"serializer:json"`
	PageCitationsTruncated bool     `json:"page_citations_truncated"`
	Path                   string   `json:"path"`
	BlobSHA                string   `json:"blob_sha"`
	Title                  string   `json:"title"`
	Heading                string   `json:"heading"`
	Section                int      `json:"section"`
	Season                 string   `json:"season,omitempty"`
	Content                string   `json:"content"`
	Score                  float64  `json:"score"`
	Truncated              bool     `json:"truncated"`
	SourceRecordIDs        []string `json:"source_record_ids"`
}

type WikiIndexedSearch struct {
	WikiFreshness
	Results          []WikiSectionHit    `json:"results"`
	Citations        map[string]Citation `json:"citations"`
	MissingCitations []string            `json:"missing_citations"`
	Retrieval        string              `json:"retrieval"`
}

func NewWikiIndex(db *gorm.DB, source snapshotRepository) *WikiIndex {
	return &WikiIndex{db: db, source: source, wake: make(chan struct{}, 1)}
}

func (i *WikiIndex) Run(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(wikiRefreshInterval)
	defer ticker.Stop()
	for {
		if err := i.Refresh(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-i.wake:
		}
	}
}

func (i *WikiIndex) Refresh(ctx context.Context) error {
	i.refresh.Lock()
	defer i.refresh.Unlock()
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	snapshot, err := i.source.Snapshot(refreshCtx)
	if err != nil {
		updateErr := i.db.WithContext(ctx).Model(&model.WikiIndexState{}).Where("id = ?", "wiki").Updates(map[string]any{"last_error": "Wiki refresh failed; serving the last indexed revision."}).Error
		return errors.Join(fmt.Errorf("refresh wiki: %w", err), updateErr)
	}
	err = i.install(ctx, snapshot)
	if err != nil {
		updateErr := i.db.WithContext(ctx).Model(&model.WikiIndexState{}).Where("id = ?", "wiki").Update("last_error", "Wiki indexing failed; serving the last indexed revision.").Error
		return errors.Join(err, updateErr)
	}
	return nil
}

func (i *WikiIndex) install(ctx context.Context, snapshot repository.Snapshot) error {
	if !repository.ValidCommit(snapshot.CommitSHA) {
		return ErrInvalid
	}
	return i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(72410311)").Error; err != nil {
			return err
		}
		var state model.WikiIndexState
		err := tx.First(&state, "id = ?", "wiki").Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		if err == nil && state.CommitSHA == snapshot.CommitSHA && state.Version == wikiIndexVersion {
			return tx.Model(&state).Updates(map[string]any{"checked_at": now, "last_error": ""}).Error
		}
		previous := []model.WikiPage{}
		if err := tx.Find(&previous).Error; err != nil {
			return err
		}
		oldPages := make(map[string]model.WikiPage, len(previous))
		for _, page := range previous {
			oldPages[page.Path] = page
		}
		seen := map[string]bool{}
		for _, page := range snapshot.Pages {
			if !repository.ValidPagePath(page.Path) || len(page.Content) > repository.MaxPageBytes || !utf8.ValidString(page.Content) || strings.ContainsRune(page.Content, 0) || seen[page.Path] {
				return ErrInvalid
			}
			seen[page.Path] = true
			if previous, ok := oldPages[page.Path]; ok && previous.BlobSHA == page.BlobSHA && state.Version == wikiIndexVersion {
				continue
			}
			title, season, sections := parseWikiPage(page)
			ids := citationIDs([]string{page.Content})
			stored := model.WikiPage{Path: page.Path, BlobSHA: page.BlobSHA, Title: title, Content: page.Content, Season: season, SourceRecordIDs: ids[:min(len(ids), 16)], CitationsTruncated: len(ids) > 16}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&stored).Error; err != nil {
				return err
			}
			if err := tx.Where("path = ?", page.Path).Delete(&model.WikiSection{}).Error; err != nil {
				return err
			}
			if len(sections) > 0 {
				if err := tx.CreateInBatches(sections, 100).Error; err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE knowledge_wiki_section SET search_vector =
         setweight(to_tsvector('english', ?), 'A') || setweight(to_tsvector('simple', ?), 'A') ||
         setweight(to_tsvector('english', heading), 'B') || setweight(to_tsvector('english', content), 'D')
         WHERE path = ?`, title, page.Path, page.Path).Error; err != nil {
					return err
				}
			}
		}
		for _, page := range previous {
			if !seen[page.Path] {
				if err := tx.Where("path = ?", page.Path).Delete(&model.WikiSection{}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&page).Error; err != nil {
					return err
				}
			}
		}
		state = model.WikiIndexState{Version: wikiIndexVersion, ID: "wiki", CommitSHA: snapshot.CommitSHA, IndexedAt: now, CheckedAt: now}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&state).Error
	})
}

func indexFreshness(tx *gorm.DB) (WikiFreshness, error) {
	var state model.WikiIndexState
	if err := tx.First(&state, "id = ?", "wiki").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WikiFreshness{}, ErrUnavailable
		}
		return WikiFreshness{}, err
	}
	return WikiFreshness{CommitSHA: state.CommitSHA, IndexedAt: state.IndexedAt, CheckedAt: state.CheckedAt,
		Stale: state.LastError != "" || time.Since(state.CheckedAt) > 2*wikiRefreshInterval, RefreshError: state.LastError}, nil
}

func (s *WikiService) ReadIndexedPage(ctx context.Context, path string, options WikiReadOptions) (WikiIndexedPage, error) {
	if !repository.ValidPagePath(path) || !repository.ValidCommit(options.CommitSHA) || options.Cursor < 0 || options.MaxBytes < 0 || options.MaxBytes > 32000 || options.Section != nil && *options.Section < 0 {
		return WikiIndexedPage{}, ErrInvalid
	}
	result := WikiIndexedPage{Outline: []WikiOutline{}, MissingCitations: []string{}}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshness, err := indexFreshness(tx)
		if err != nil {
			return err
		}
		if options.CommitSHA != "" && options.CommitSHA != freshness.CommitSHA {
			return ErrConflict
		}
		result.WikiFreshness = freshness
		var page model.WikiPage
		if err := tx.First(&page, "path = ?", path).Error; err != nil {
			return mapDatabaseError(err)
		}
		sections := []model.WikiSection{}
		if err := tx.Omit("search_vector").Where("path = ?", path).Order("position").Find(&sections).Error; err != nil {
			return err
		}
		content := page.Content
		result.TotalSections = len(sections)
		result.OutlineTruncated = len(sections) > 100
		for _, section := range sections[:min(len(sections), 100)] {
			result.Outline = append(result.Outline, WikiOutline{Position: section.Position, Heading: section.Heading, Bytes: section.End - section.Start})
		}
		if options.Section != nil {
			if *options.Section >= len(sections) {
				return ErrNotFound
			}
			section := sections[*options.Section]
			content = page.Content[section.Start:section.End]
		}
		if options.Cursor > len(content) || options.Cursor < len(content) && !utf8.RuneStart(content[options.Cursor]) {
			return ErrInvalid
		}
		limit := options.MaxBytes
		if limit == 0 {
			limit = wikiReadLimit
		}
		end := min(len(content), options.Cursor+limit)
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if end == options.Cursor && end < len(content) {
			return ErrInvalid
		}
		result.Page = repository.Page{Path: path, BlobSHA: page.BlobSHA, Content: content[options.Cursor:end]}
		result.Title, result.Season = page.Title, page.Season
		result.Cursor, result.TotalBytes = options.Cursor, len(content)
		result.Truncated = end < len(content)
		if result.Truncated {
			result.NextCursor = &end
		}
		resolver := NewWikiService(tx, s.wiki)
		result.PageSourceRecordIDs = page.SourceRecordIDs
		result.PageCitationsTruncated = page.CitationsTruncated
		references := []string{result.Page.Content, strings.Join(page.SourceRecordIDs, " ")}
		result.Citations, err = resolver.resolveCitations(ctx, references)
		result.MissingCitations = missingCitations(references, result.Citations)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func (s *WikiService) SearchIndexed(ctx context.Context, options WikiSearchOptions) (WikiIndexedSearch, error) {
	started := time.Now()
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" || len(options.Query) > 256 || strings.ContainsRune(options.Query, 0) || options.Limit < 1 || options.Limit > 20 || len(options.Prefix) > 512 || options.Season != "" && !seasonPattern.MatchString(options.Season) {
		return WikiIndexedSearch{}, ErrInvalid
	}
	result := WikiIndexedSearch{Results: []WikiSectionHit{}, MissingCitations: []string{}, Retrieval: "lexical_v1"}
	defer func() {
		if logger.SugarLogger != nil {
			bytes, allTerms := 0, 0
			for _, hit := range result.Results {
				bytes += len(hit.Content)
				if hit.MatchedAllTerms {
					allTerms++
				}
			}
			logger.SugarLogger.Infow("wiki retrieval", "query_hash", hashBytes([]byte(options.Query)),
				"duration_ms", time.Since(started).Milliseconds(), "revision", result.CommitSHA, "stale", result.Stale,
				"hits", len(result.Results), "all_term_hits", allTerms, "content_bytes", bytes,
				"missing_citations", len(result.MissingCitations), "retrieval", result.Retrieval)
		}
	}()
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshness, err := indexFreshness(tx)
		if err != nil {
			return err
		}
		result.WikiFreshness = freshness
		var hits []WikiSectionHit
		err = tx.Raw(`WITH query AS (
      SELECT plainto_tsquery('english', ?) AS all_terms,
      replace(plainto_tsquery('english', ?)::text, ' & ', ' | ')::tsquery AS any_terms
    ) SELECT section.path, page.blob_sha, page.title, page.source_record_ids AS page_source_record_ids,
 page.citations_truncated AS page_citations_truncated, section.search_vector @@ query.all_terms AS matched_all_terms,
 section.heading, section.position AS section, section.season,
      section.content, ts_rank_cd(section.search_vector, query.any_terms, 32) +
      CASE WHEN section.search_vector @@ query.all_terms THEN 1 ELSE 0 END AS score
      FROM knowledge_wiki_section section JOIN knowledge_wiki_page page USING(path) CROSS JOIN query
      WHERE section.search_vector @@ query.any_terms AND (? = '' OR section.season = ?)
      AND starts_with(section.path, ?)
      ORDER BY score DESC, section.path, section.position LIMIT ?`, options.Query, options.Query, options.Season, options.Season, options.Prefix, options.Limit).Scan(&hits).Error
		if err != nil {
			return err
		}
		budget := wikiSearchBudget
		if options.budget > 0 {
			budget = min(budget, options.budget)
		}
		contents := []string{}
		for _, hit := range hits {
			if budget <= 0 {
				break
			}
			original := hit.Content
			limit := min(len(original), min(3000, budget))
			for limit < len(original) && !utf8.RuneStart(original[limit]) {
				limit--
			}
			hit.Content, hit.Truncated = original[:limit], limit < len(original)
			budget -= limit
			hit.SourceRecordIDs = citationIDs([]string{hit.Content})
			contents = append(contents, hit.Content, strings.Join(hit.PageSourceRecordIDs, " "))
			result.Results = append(result.Results, hit)
		}
		resolver := NewWikiService(tx, s.wiki)
		result.Citations, err = resolver.resolveCitations(ctx, contents)
		result.MissingCitations = missingCitations(contents, result.Citations)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func citationIDs(contents []string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, content := range contents {
		for _, id := range sourceRecordReferencePattern.FindAllString(content, -1) {
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}
func missingCitations(contents []string, citations map[string]Citation) []string {
	missing := []string{}
	for _, id := range citationIDs(contents) {
		if _, ok := citations[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

type WikiPageListing struct {
	WikiFreshness
	Pages      []repository.Page `json:"pages"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (s *WikiService) ListIndexedPages(ctx context.Context, prefix, after string) (WikiPageListing, error) {
	if len(prefix) > 512 || len(after) > 512 {
		return WikiPageListing{}, ErrInvalid
	}
	result := WikiPageListing{Pages: []repository.Page{}}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshness, err := indexFreshness(tx)
		if err != nil {
			return err
		}
		result.WikiFreshness = freshness
		var pages []model.WikiPage
		if err := tx.Select("path", "blob_sha").Where("starts_with(path, ?) AND path > ?", prefix, after).Order("path").Limit(101).Find(&pages).Error; err != nil {
			return err
		}
		if len(pages) > 100 {
			result.NextCursor = pages[99].Path
			pages = pages[:100]
		}
		for _, page := range pages {
			result.Pages = append(result.Pages, repository.Page{Path: page.Path, BlobSHA: page.BlobSHA})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func (s *WikiService) SetIndex(index *WikiIndex) { s.index = index }
func (i *WikiIndex) Wake() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}
