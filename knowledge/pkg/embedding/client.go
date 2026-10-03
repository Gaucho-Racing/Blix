package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Endpoint       string
	APIKey         string
	Model          string
	Dimensions     int
	SendDimensions bool
	HTTP           *http.Client
}

func (c *Client) ModelName() string { return c.Model }

func New(endpoint, key, model string, dimensions int) *Client {
	return &Client{Endpoint: strings.TrimRight(endpoint, "/"), APIKey: key, Model: model, Dimensions: dimensions, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > 32 {
		return nil, fmt.Errorf("embedding batch must contain 1 to 32 inputs")
	}
	for _, s := range texts {
		if strings.TrimSpace(s) == "" || len(s) > 8000 {
			return nil, fmt.Errorf("embedding input must contain 1 to 8000 bytes")
		}
	}
	payload := map[string]any{"model": c.Model, "input": texts, "encoding_format": "float"}
	if c.SendDimensions {
		payload["dimensions"] = c.Dimensions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Blix-Knowledge/1.0")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding provider returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Data []struct {
			Index  int       `json:"index"`
			Vector []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&response); err != nil {
		return nil, fmt.Errorf("invalid embedding response")
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response count mismatch")
	}
	vectors := make([][]float32, len(texts))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(texts) || vectors[item.Index] != nil || len(item.Vector) != c.Dimensions {
			return nil, fmt.Errorf("embedding response index or dimensions mismatch")
		}
		norm := 0.0
		for _, v := range item.Vector {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("embedding response contains nonfinite values")
			}
			norm += float64(v) * float64(v)
		}
		if norm == 0 {
			return nil, fmt.Errorf("embedding response contains a zero vector")
		}
		vectors[item.Index] = item.Vector
	}
	return vectors, nil
}

func (c *Client) Namespace(revision string) string {
	data, _ := json.Marshal([]any{c.Endpoint, c.Model, revision, c.Dimensions, c.SendDimensions})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
