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
)

type DocumentRuntimeClient struct {
	baseURL, token string
	http           *http.Client
}

func NewDocumentRuntimeClient(baseURL, token string) *DocumentRuntimeClient {
	return &DocumentRuntimeClient{strings.TrimRight(baseURL, "/"), token, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *DocumentRuntimeClient) request(ctx context.Context, method, path string, payload any, output any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
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
