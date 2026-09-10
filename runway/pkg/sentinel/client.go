package sentinel

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

type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	redirectURI  string
	http         *http.Client
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type Claims struct {
	EntityID string
	UserID   string
	Audience []string
	Scope    string
	Groups   []string
}

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("sentinel returned HTTP %d: %s", e.Status, e.Message)
}

func NewClient(baseURL string, clientID string, clientSecret string, redirectURI string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		redirectURI:  strings.TrimSpace(redirectURI),
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c.baseURL != "" && c.clientID != "" && c.clientSecret != "" && c.redirectURI != ""
}

func (c *Client) ClientID() string {
	return c.clientID
}

func (c *Client) RedirectURI() string {
	return c.redirectURI
}

func (c *Client) AuthorizationURL(state string, scope string) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("sentinel OAuth is not configured")
	}
	values := url.Values{}
	values.Set("client_id", c.clientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", c.redirectURI)
	values.Set("scope", scope)
	values.Set("state", state)
	values.Set("prompt", "none")
	return c.baseURL + "/oauth/authorize?" + values.Encode(), nil
}

func (c *Client) ExchangeAuthorizationCode(ctx context.Context, code string) (TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURI)
	return c.exchange(ctx, form)
}

func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.exchange(ctx, form)
}

func (c *Client) ValidateToken(ctx context.Context, token string) (Claims, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return Claims{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/api/core/token/validate",
		bytes.NewReader(body),
	)
	if err != nil {
		return Claims{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	responseBody, err := c.do(request)
	if err != nil {
		return Claims{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(responseBody, &raw); err != nil {
		return Claims{}, fmt.Errorf("decode Sentinel token claims: %w", err)
	}
	claims := Claims{
		EntityID: stringClaim(raw, "sub"),
		UserID:   stringClaim(raw, "user_id"),
		Audience: stringSliceClaim(raw, "aud"),
		Scope:    stringClaim(raw, "scope"),
		Groups:   stringSliceClaim(raw, "groups"),
	}
	if claims.EntityID == "" {
		return Claims{}, fmt.Errorf("sentinel token subject is missing")
	}
	return claims, nil
}

func (c *Client) exchange(ctx context.Context, form url.Values) (TokenResponse, error) {
	if !c.Configured() {
		return TokenResponse{}, fmt.Errorf("sentinel OAuth is not configured")
	}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/api/oauth/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return TokenResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	responseBody, err := c.do(request)
	if err != nil {
		return TokenResponse{}, err
	}
	var token TokenResponse
	if err := json.Unmarshal(responseBody, &token); err != nil {
		return TokenResponse{}, fmt.Errorf("decode Sentinel token response: %w", err)
	}
	if token.AccessToken == "" {
		return TokenResponse{}, fmt.Errorf("sentinel token response did not include an access token")
	}
	return token, nil
}

func (c *Client) do(request *http.Request) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("sentinel request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Sentinel response: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		var payload struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &payload)
		message := payload.ErrorDescription
		if message == "" {
			message = payload.Error
		}
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		return nil, &Error{Status: response.StatusCode, Message: message}
	}
	return body, nil
}

func stringClaim(claims map[string]any, name string) string {
	value, _ := claims[name].(string)
	return value
}

func stringSliceClaim(claims map[string]any, name string) []string {
	switch values := claims[name].(type) {
	case string:
		return []string{values}
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
