package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
)

type DocumentRuntimeClient struct {
	baseURL, token string
	http           *http.Client
}

func NewDocumentRuntimeClient(baseURL, token string) *DocumentRuntimeClient {
	return &DocumentRuntimeClient{strings.TrimRight(baseURL, "/"), token, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *DocumentRuntimeClient) request(ctx context.Context, method, path string, payload any, output any) error {
	var data []byte
	var err error
	if payload != nil {
		data, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/internal/v1/"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: Runway is unavailable", ErrUnavailable)
	}
	defer res.Body.Close()
	if res.StatusCode == 409 {
		return ErrConflict
	}
	if res.StatusCode == 400 {
		return ErrInvalid
	}
	if res.StatusCode == 403 {
		return ErrForbidden
	}
	if res.StatusCode == 404 {
		return ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: Runway returned HTTP %d", ErrUnavailable, res.StatusCode)
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4*1024*1024)).Decode(output)
}

type DocumentExecution struct {
	AttemptID    string `json:"attempt_id"`
	WorkerID     string `json:"worker_id"`
	FencingToken string `json:"fencing_token"`
	Generation   uint64 `json:"generation"`
}
type validatedDocumentExecution struct {
	TaskID         string `json:"task_id"`
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	AttemptNumber  uint64 `json:"attempt_number"`
	Input          struct {
		DocumentID string `json:"document_id"`
		Generation uint64 `json:"generation"`
	} `json:"input"`
}

func (c *DocumentRuntimeClient) validate(ctx context.Context, id string, e DocumentExecution) (validatedDocumentExecution, error) {
	var result validatedDocumentExecution
	err := c.request(ctx, "POST", "document-attempts/validate", e, &result)
	if err == nil && (result.Input.DocumentID != id || result.Input.Generation != e.Generation) {
		err = ErrForbidden
	}
	return result, err
}
func (c *DocumentRuntimeClient) control(ctx context.Context, task, owner, operation string) error {
	return c.request(ctx, "POST", "tasks/"+url.PathEscape(task)+"/"+operation, map[string]string{"owner": owner}, nil)
}

func (s *DocumentService) ConversationRetrievalScope(ctx context.Context, conversationID, sessionID, scope string) (DocumentRetrievalScope, error) {
	if scope != "conversation" && scope != "team" {
		return DocumentRetrievalScope{}, ErrInvalid
	}
	if s.runtime == nil {
		return DocumentRetrievalScope{}, ErrUnavailable
	}
	if !identifier.IsValid(conversationID, identifier.Conversation) || !identifier.IsValid(sessionID, identifier.Session) {
		return DocumentRetrievalScope{}, ErrInvalid
	}
	access, err := s.agentDocumentContext(ctx, conversationID, sessionID)
	if err != nil {
		return DocumentRetrievalScope{}, err
	}
	result := DocumentRetrievalScope{DocumentIDs: access.DocumentIDs, Team: scope == "team"}
	if access.ProfileID == "document_qa_v1" {
		if len(access.DocumentIDs) != 1 {
			return DocumentRetrievalScope{}, ErrForbidden
		}
		result.Team = false
		result.AllowUnapproved = true
	}
	return result, nil
}

type agentDocumentAccess struct {
	DocumentIDs []string `json:"document_ids"`
	ProfileID   string   `json:"profile_id"`
}

func (s *DocumentService) agentDocumentContext(ctx context.Context, conversationID, sessionID string) (agentDocumentAccess, error) {
	var result agentDocumentAccess
	if s.runtime == nil {
		return result, ErrUnavailable
	}
	if !identifier.IsValid(conversationID, identifier.Conversation) || !identifier.IsValid(sessionID, identifier.Session) {
		return result, ErrInvalid
	}
	err := s.runtime.request(ctx, "GET", "conversations/"+url.PathEscape(conversationID)+"/retrieval-context?session_id="+url.QueryEscape(sessionID), nil, &result)
	if err == nil {
		err = validateRetrievalIDs(result.DocumentIDs)
	}
	return result, err
}
