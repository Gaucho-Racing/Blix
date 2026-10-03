package client

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

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string   { return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status) }
func (e *Error) Retryable() bool { return e.Status == 0 || e.Status == 429 || e.Status >= 500 }

type HTTP struct {
	BaseURL, Token string
	Client         *http.Client
}

func NewHTTP(base, token string) *HTTP {
	return &HTTP{BaseURL: strings.TrimRight(base, "/"), Token: token, Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *HTTP) Request(ctx context.Context, method, path string, input, output any, headers map[string]string) error {
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return &Error{Code: "upstream_unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &Error{Status: res.StatusCode, Code: "upstream_request_failed"}
	}
	if output == nil {
		return nil
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(output); err != nil {
		return &Error{Code: "invalid_upstream_response"}
	}
	return nil
}
