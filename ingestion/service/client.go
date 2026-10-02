package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPError struct {
	Status  int
	Service string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%s returned HTTP %d", e.Service, e.Status) }

type Client struct {
	baseURL, token, name string
	http                 *http.Client
}

func NewClient(baseURL, token, name string) *Client {
	return &Client{strings.TrimRight(baseURL, "/"), token, name, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) request(ctx context.Context, method, path string, payload, output any) error {
	res, err := c.send(ctx, method, path, payload)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2*1024*1024)).Decode(output)
}
func (c *Client) send(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var data []byte
	var err error
	if payload != nil {
		data, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/internal/v1/"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", c.name, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &HTTPError{res.StatusCode, c.name}
	}
	return res, nil
}
