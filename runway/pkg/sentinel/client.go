package sentinel

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken           = errors.New("invalid sentinel token")
	ErrSigningKeysUnavailable = errors.New("sentinel signing keys unavailable")
)

const signingKeyRefetchFloor = time.Minute

type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	redirectURI  string
	http         *http.Client
	keyMu        sync.RWMutex
	keys         map[string]*rsa.PublicKey
	lastKeyFetch time.Time
	keyFetchErr  error
	keyFetchMu   sync.Mutex
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

func (c *Client) InitializeSigningKeys(ctx context.Context) error {
	return c.refreshSigningKeys(ctx, true)
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
	tokenClaims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(
		token,
		tokenClaims,
		func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			return c.signingKey(ctx, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithAudience(c.clientID),
	)
	if err != nil {
		if errors.Is(err, ErrSigningKeysUnavailable) {
			return Claims{}, err
		}
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return Claims{}, ErrInvalidToken
	}
	raw := map[string]any(tokenClaims)
	claims := Claims{
		EntityID: stringClaim(raw, "sub"),
		UserID:   stringClaim(raw, "user_id"),
		Audience: stringSliceClaim(raw, "aud"),
		Scope:    stringClaim(raw, "scope"),
		Groups:   stringSliceClaim(raw, "groups"),
	}
	if claims.EntityID == "" {
		return Claims{}, fmt.Errorf("%w: token subject is missing", ErrInvalidToken)
	}
	return claims, nil
}

func (c *Client) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("%w: token key id is missing", ErrInvalidToken)
	}
	if key := c.cachedSigningKey(kid); key != nil {
		return key, nil
	}
	if err := c.refreshSigningKeys(ctx, false); err != nil {
		return nil, err
	}
	if key := c.cachedSigningKey(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("%w: no signing key matches kid %q", ErrInvalidToken, kid)
}

func (c *Client) cachedSigningKey(kid string) *rsa.PublicKey {
	c.keyMu.RLock()
	defer c.keyMu.RUnlock()
	return c.keys[kid]
}

func (c *Client) refreshSigningKeys(ctx context.Context, force bool) error {
	c.keyFetchMu.Lock()
	defer c.keyFetchMu.Unlock()

	c.keyMu.RLock()
	lastFetch := c.lastKeyFetch
	lastError := c.keyFetchErr
	c.keyMu.RUnlock()
	if !force && !lastFetch.IsZero() && time.Since(lastFetch) < signingKeyRefetchFloor {
		return lastError
	}

	keys, fetchErr := c.fetchSigningKeys(ctx)
	var err error
	if fetchErr != nil {
		err = fmt.Errorf("%w: %v", ErrSigningKeysUnavailable, fetchErr)
	}
	c.keyMu.Lock()
	c.lastKeyFetch = time.Now()
	c.keyFetchErr = err
	if err == nil {
		c.keys = keys
	}
	c.keyMu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) fetchSigningKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/core/keys", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	body, err := c.do(request)
	if err != nil {
		return nil, err
	}
	var document struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			ID        string `json:"kid"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("decode Sentinel JWKS: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, encoded := range document.Keys {
		if encoded.KeyType != "RSA" || encoded.Algorithm != "RS256" || encoded.Use != "sig" || encoded.ID == "" {
			continue
		}
		key, err := decodeRSAKey(encoded.Modulus, encoded.Exponent)
		if err != nil {
			return nil, fmt.Errorf("decode Sentinel signing key %q: %w", encoded.ID, err)
		}
		keys[encoded.ID] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("Sentinel JWKS contains no RS256 signing keys")
	}
	return keys, nil
}

func decodeRSAKey(modulus string, exponent string) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil || len(n) == 0 {
		return nil, errors.New("invalid RSA modulus")
	}
	e, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("invalid RSA exponent")
	}
	exponentValue := new(big.Int).SetBytes(e)
	if !exponentValue.IsInt64() || exponentValue.Int64() < 2 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponentValue.Int64())}, nil
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
