package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

type DocumentScopeValidator interface {
	ValidateDocuments(context.Context, []string) error
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
	if c.url == "" || c.token == "" {
		return ErrUnavailable
	}
	data, err := json.Marshal(map[string]any{"document_ids": ids})
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
}

func (s *ExecutionService) RetrievalContext(ctx context.Context, conversationID, sessionID string) (RetrievalContext, error) {
	if !identifier.IsValid(conversationID, identifier.Conversation) || !identifier.IsValid(sessionID, identifier.Session) {
		return RetrievalContext{}, ErrInvalid
	}
	var conversation model.Conversation
	if err := s.database.WithContext(ctx).First(&conversation, "id = ?", conversationID).Error; err != nil {
		return RetrievalContext{}, mapDatabaseError(err)
	}
	var count int64
	if err := s.database.WithContext(ctx).Model(&model.AgentSession{}).Where("id = ? AND conversation_id = ?", sessionID, conversationID).Count(&count).Error; err != nil {
		return RetrievalContext{}, err
	}
	if count != 1 || conversation.CallerContext.Data().EntityType != model.EntityTypeUser {
		return RetrievalContext{}, ErrForbidden
	}
	return RetrievalContext{DocumentIDs: append([]string{}, conversation.DocumentIDs...)}, nil
}
