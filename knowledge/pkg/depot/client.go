package depot

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

type File struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}
type Upload struct {
	File      File      `json:"file"`
	URL       string    `json:"upload_url"`
	Method    string    `json:"method"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Client struct {
	baseURL, bucket, token string
	http                   *http.Client
}

func NewClient(baseURL, bucket, token string) *Client {
	return &Client{strings.TrimRight(baseURL, "/"), bucket, token, &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Configured() bool { return c.baseURL != "" && c.bucket != "" && c.token != "" }
func (c *Client) request(ctx context.Context, method, path string, body any, output any) error {
	if !c.Configured() {
		return fmt.Errorf("Depot URL, bucket, and Sentinel service account token are required")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Depot request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Depot returned HTTP %d", res.StatusCode)
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(output)
}
func (c *Client) bucketPath() string { return "/buckets/" + url.PathEscape(c.bucket) }
func (c *Client) Initiate(ctx context.Context, filename, documentID string) (Upload, error) {
	var result Upload
	err := c.request(ctx, "POST", c.bucketPath()+"/uploads", map[string]any{"original_name": filename, "content_type": "application/pdf", "public": false, "tags": map[string]string{"blix_document_id": documentID}}, &result)
	if err == nil && (result.File.ID == "" || result.Method != "PUT" || result.URL == "") {
		err = fmt.Errorf("Depot returned an invalid upload intent")
	}
	return result, err
}
func (c *Client) Complete(ctx context.Context, id string) (File, error) {
	var result File
	err := c.request(ctx, "POST", c.bucketPath()+"/uploads/"+url.PathEscape(id)+"/complete", nil, &result)
	return result, err
}
func (c *Client) Download(ctx context.Context, id string, maxBytes int64) ([]byte, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Depot is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/download/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Depot download failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Depot returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("Depot file exceeds document size limit")
	}
	return data, nil
}
