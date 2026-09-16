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

	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

type KnowledgeService struct {
	baseURL string
	client  *http.Client
}

func NewKnowledgeService(baseURL string) *KnowledgeService {
	return &KnowledgeService{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{
		Timeout:       25 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *KnowledgeService) ListChangesets(ctx context.Context, limit int, status, before string) (json.RawMessage, error) {
	query := url.Values{"limit": {fmt.Sprint(limit)}, "status": {status}, "before": {before}}
	return s.request(ctx, http.MethodGet, "changesets", query, nil)
}

func (s *KnowledgeService) GetChangeset(ctx context.Context, id string, basePath *string) (json.RawMessage, error) {
	if !identifier.IsValid(id, identifier.Changeset) {
		return nil, ErrInvalid
	}
	path, query := "changesets/"+id, url.Values{}
	if basePath != nil {
		path += "/base"
		query.Set("path", *basePath)
	}
	return s.request(ctx, http.MethodGet, path, query, nil)
}

func (s *KnowledgeService) ReviewChangeset(ctx context.Context, id, decision, entityID, note string) (json.RawMessage, error) {
	if !identifier.IsValid(id, identifier.Changeset) || !identifier.IsValid(entityID, identifier.Entity) || (decision != "approve" && decision != "reject") {
		return nil, ErrInvalid
	}
	payload, err := json.Marshal(map[string]string{"reviewer_entity_id": entityID, "note": note})
	if err != nil {
		return nil, err
	}
	return s.request(ctx, http.MethodPost, "changesets/"+id+"/review/"+decision, nil, payload)
}

func (s *KnowledgeService) request(ctx context.Context, method, path string, query url.Values, payload []byte) (json.RawMessage, error) {
	endpoint := s.baseURL + "/internal/v1/" + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: knowledge service is unavailable; refresh the changeset before retrying", ErrUnavailable)
	}
	defer response.Body.Close()
	const maxResponseBytes = 32 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || !json.Valid(body) {
		return nil, fmt.Errorf("%w: invalid knowledge response", ErrUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		switch response.StatusCode {
		case 400:
			return nil, fmt.Errorf("%w: %s", ErrInvalid, failure.Error)
		case 404:
			return nil, ErrNotFound
		case 409:
			return nil, fmt.Errorf("%w: %s", ErrConflict, failure.Error)
		default:
			return nil, fmt.Errorf("%w: knowledge service failed; refresh the changeset before retrying", ErrUnavailable)
		}
	}
	return body, nil
}
