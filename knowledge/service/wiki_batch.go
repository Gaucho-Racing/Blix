package service

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

type WikiSearchBatch struct {
	Queries []string `json:"queries"`
	Limit   int      `json:"limit"`
	Season  string   `json:"season,omitempty"`
	Prefix  string   `json:"prefix,omitempty"`
}

type WikiPageBatch struct {
	Paths     []string `json:"paths"`
	CommitSHA string   `json:"commit_sha,omitempty"`
}

type WikiBatchItem[T any] struct {
	Result *T     `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type WikiBatchResult[T any] struct {
	WikiFreshness
	Results []WikiBatchItem[T] `json:"results"`
}

func (s *WikiService) SearchBatch(ctx context.Context, input WikiSearchBatch) (WikiBatchResult[WikiIndexedSearch], error) {
	if len(input.Queries) < 1 || len(input.Queries) > 6 {
		return WikiBatchResult[WikiIndexedSearch]{}, ErrInvalid
	}
	if input.Limit == 0 {
		input.Limit = 8
	}
	result := WikiBatchResult[WikiIndexedSearch]{Results: []WikiBatchItem[WikiIndexedSearch]{}}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshness, err := indexFreshness(tx)
		if err != nil {
			return err
		}
		result.WikiFreshness = freshness
		reader := NewWikiService(tx, s.wiki)
		for _, query := range input.Queries {
			found, err := reader.SearchIndexed(ctx, WikiSearchOptions{Query: query, Limit: input.Limit, Season: input.Season, Prefix: input.Prefix, budget: wikiSearchBudget / len(input.Queries)})
			if err != nil {
				if !errors.Is(err, ErrInvalid) {
					return err
				}
				result.Results = append(result.Results, WikiBatchItem[WikiIndexedSearch]{Error: "invalid_knowledge_query"})
			} else {
				result.Results = append(result.Results, WikiBatchItem[WikiIndexedSearch]{Result: &found})
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func (s *WikiService) ReadPageBatch(ctx context.Context, input WikiPageBatch) (WikiBatchResult[WikiIndexedPage], error) {
	if len(input.Paths) < 1 || len(input.Paths) > 10 {
		return WikiBatchResult[WikiIndexedPage]{}, ErrInvalid
	}
	result := WikiBatchResult[WikiIndexedPage]{Results: []WikiBatchItem[WikiIndexedPage]{}}
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshness, err := indexFreshness(tx)
		if err != nil {
			return err
		}
		result.WikiFreshness = freshness
		reader := NewWikiService(tx, s.wiki)
		for _, path := range input.Paths {
			page, err := reader.ReadIndexedPage(ctx, path, WikiReadOptions{CommitSHA: input.CommitSHA, MaxBytes: min(wikiReadLimit, 24000/len(input.Paths))})
			if err != nil {
				code := ""
				switch {
				case errors.Is(err, ErrNotFound):
					code = "knowledge_not_found"
				case errors.Is(err, ErrInvalid):
					code = "invalid_knowledge_request"
				case errors.Is(err, ErrConflict):
					code = "knowledge_conflict"
				default:
					return err
				}
				result.Results = append(result.Results, WikiBatchItem[WikiIndexedPage]{Error: code})
			} else {
				result.Results = append(result.Results, WikiBatchItem[WikiIndexedPage]{Result: &page})
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
