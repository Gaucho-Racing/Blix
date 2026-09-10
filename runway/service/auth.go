package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"github.com/gaucho-racing/blix/runway/pkg/sentinel"
)

const cliCallbackPath = "/blix-cli/callback"

type AuthService struct {
	sentinel *sentinel.Client
	scopes   string
}

type CallerContext struct {
	EntityID string   `json:"entity_id"`
	UserID   string   `json:"user_id,omitempty"`
	Scope    string   `json:"scope"`
	Groups   []string `json:"groups"`
}

func NewAuthService(client *sentinel.Client, scopes string) *AuthService {
	return &AuthService{sentinel: client, scopes: strings.TrimSpace(scopes)}
}

func (s *AuthService) AuthorizationURL(state string, redirectURI string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("%w: state is required", ErrInvalid)
	}
	if err := validateCLIRedirectURI(redirectURI); err != nil {
		return "", err
	}
	value, err := s.sentinel.AuthorizationURL(state, s.scopes, redirectURI)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return value, nil
}

func (s *AuthService) ExchangeAuthorizationCode(
	ctx context.Context,
	code string,
	redirectURI string,
) (sentinel.TokenResponse, error) {
	if strings.TrimSpace(code) == "" {
		return sentinel.TokenResponse{}, fmt.Errorf("%w: code is required", ErrInvalid)
	}
	if err := validateCLIRedirectURI(redirectURI); err != nil {
		return sentinel.TokenResponse{}, err
	}
	token, err := s.sentinel.ExchangeAuthorizationCode(ctx, code, redirectURI)
	if err != nil {
		return sentinel.TokenResponse{}, mapSentinelAuthError(err)
	}
	return token, nil
}

func validateCLIRedirectURI(raw string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || !strings.EqualFold(parsed.Hostname(), "localhost") {
		return fmt.Errorf("%w: redirect_uri must be an HTTP localhost URL", ErrInvalid)
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("%w: redirect_uri must include a valid port", ErrInvalid)
	}
	if parsed.User != nil || parsed.EscapedPath() != cliCallbackPath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: redirect_uri must use %s", ErrInvalid, cliCallbackPath)
	}
	return nil
}

func (s *AuthService) RefreshToken(ctx context.Context, refreshToken string) (sentinel.TokenResponse, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return sentinel.TokenResponse{}, fmt.Errorf("%w: refresh_token is required", ErrInvalid)
	}
	token, err := s.sentinel.RefreshToken(ctx, refreshToken)
	if err != nil {
		return sentinel.TokenResponse{}, mapSentinelAuthError(err)
	}
	return token, nil
}

func (s *AuthService) Authenticate(ctx context.Context, token string) (CallerContext, error) {
	if strings.TrimSpace(token) == "" {
		return CallerContext{}, ErrUnauthorized
	}
	if !s.sentinel.Configured() {
		return CallerContext{}, fmt.Errorf("%w: sentinel OAuth is not configured", ErrUnavailable)
	}
	claims, err := s.sentinel.ValidateToken(ctx, token)
	if err != nil {
		return CallerContext{}, mapSentinelAuthError(err)
	}
	if !contains(claims.Audience, s.sentinel.ClientID()) {
		return CallerContext{}, fmt.Errorf("%w: token is not intended for Blix", ErrUnauthorized)
	}
	if !identifier.IsValid(claims.EntityID, identifier.Entity) {
		return CallerContext{}, fmt.Errorf("%w: token subject is invalid", ErrUnauthorized)
	}
	return CallerContext{
		EntityID: claims.EntityID,
		UserID:   claims.UserID,
		Scope:    claims.Scope,
		Groups:   claims.Groups,
	}, nil
}

func mapSentinelAuthError(err error) error {
	if errors.Is(err, sentinel.ErrInvalidToken) {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if errors.Is(err, sentinel.ErrSigningKeysUnavailable) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var sentinelError *sentinel.Error
	if errors.As(err, &sentinelError) {
		if sentinelError.Status == http.StatusBadRequest || sentinelError.Status == http.StatusUnauthorized || sentinelError.Status == http.StatusForbidden {
			return fmt.Errorf("%w: %s", ErrUnauthorized, sentinelError.Message)
		}
	}
	if strings.Contains(err.Error(), "not configured") {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
