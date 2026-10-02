package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/gorm"
)

type DocumentScopeValidator interface {
	ValidateDocuments(context.Context, []string) error
}

type DocumentQuestionValidator interface {
	ValidateQuestionDocument(context.Context, string) error
}

type KnowledgeDocumentClient struct {
	url, token string
	http       *http.Client
}

func NewKnowledgeDocumentClient(url, token string) *KnowledgeDocumentClient {
	return &KnowledgeDocumentClient{url: strings.TrimRight(url, "/"), token: token, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *ExecutionService) SetDocumentScopeValidator(validator DocumentScopeValidator) {
	s.documents = validator
}

func (c *KnowledgeDocumentClient) ValidateDocuments(ctx context.Context, ids []string) error {
	return c.validate(ctx, ids, "team")
}

func (c *KnowledgeDocumentClient) ValidateQuestionDocument(ctx context.Context, id string) error {
	return c.validate(ctx, []string{id}, "questions")
}

func (c *KnowledgeDocumentClient) validate(ctx context.Context, ids []string, mode string) error {
	if c.url == "" || c.token == "" {
		return ErrUnavailable
	}
	data, err := json.Marshal(map[string]any{"document_ids": ids, "mode": mode})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/internal/v1/documents/access", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: document access could not be verified", ErrUnavailable)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusBadRequest:
		return ErrInvalid
	case http.StatusConflict:
		return ErrConflict
	default:
		return fmt.Errorf("%w: document access verification returned HTTP %d", ErrUnavailable, res.StatusCode)
	}
}

func validateDocumentIDs(ids []string) error {
	if len(ids) > 8 {
		return ErrInvalid
	}
	for i, id := range ids {
		if !identifier.IsValid(id, identifier.Prefix("document")) || slices.Contains(ids[:i], id) {
			return ErrInvalid
		}
	}
	return nil
}

type RetrievalContext struct {
	DocumentIDs []string `json:"document_ids"`
	ProfileID   string   `json:"profile_id"`
}

func (s *ExecutionService) RetrievalContext(ctx context.Context, runID, sessionID string) (RetrievalContext, error) {
	if !identifier.IsValid(runID, identifier.Run) || !identifier.IsValid(sessionID, identifier.Session) {
		return RetrievalContext{}, ErrInvalid
	}
	var run model.Run
	if err := s.database.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		return RetrievalContext{}, mapDatabaseError(err)
	}
	var session model.AgentSession
	if err := s.database.WithContext(ctx).First(&session, "id = ? AND run_id = ?", sessionID, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RetrievalContext{}, ErrForbidden
		}
		return RetrievalContext{}, err
	}
	if run.CallerContext.Data().EntityType != model.EntityTypeUser {
		return RetrievalContext{}, ErrForbidden
	}
	return RetrievalContext{DocumentIDs: append([]string{}, run.DocumentIDs...), ProfileID: session.ProfileID}, nil
}
