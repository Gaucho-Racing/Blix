package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"github.com/gaucho-racing/blix/runway/pkg/sentinel"
)

const (
	cliCallbackPath = "/blix-cli/callback"
	webCallbackPath = "/auth/login"
)

type AuthService struct {
	sentinel *sentinel.Client
	scopes   string
}

type CallerContext struct {
	EntityID         string   `json:"entity_id"`
	EntityType       string   `json:"entity_type"`
	UserID           string   `json:"user_id,omitempty"`
	ServiceAccountID string   `json:"service_account_id,omitempty"`
	Scope            string   `json:"scope"`
	Groups           []string `json:"groups"`
}

func (c CallerContext) ConversationContext() model.CallerContext {
	return model.CallerContext{
		EntityID:         c.EntityID,
		EntityType:       c.EntityType,
		UserID:           c.UserID,
		ServiceAccountID: c.ServiceAccountID,
	}
}

func NewAuthService(client *sentinel.Client, scopes string) *AuthService {
	return &AuthService{sentinel: client, scopes: strings.TrimSpace(scopes)}
}

func (s *AuthService) AuthorizationURL(state string, redirectURI string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("%w: state is required", ErrInvalid)
	}
	if err := validateOAuthRedirectURI(redirectURI); err != nil {
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
	if err := validateOAuthRedirectURI(redirectURI); err != nil {
		return sentinel.TokenResponse{}, err
	}
	token, err := s.sentinel.ExchangeAuthorizationCode(ctx, code, redirectURI)
	if err != nil {
		return sentinel.TokenResponse{}, mapSentinelAuthError(err)
	}
	return token, nil
}

func validateOAuthRedirectURI(raw string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: redirect_uri is invalid", ErrInvalid)
	}
	switch parsed.EscapedPath() {
	case cliCallbackPath:
		if parsed.Scheme != "http" || !strings.EqualFold(parsed.Hostname(), "localhost") {
			return fmt.Errorf("%w: CLI redirect_uri must be an HTTP localhost URL", ErrInvalid)
		}
		port, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("%w: CLI redirect_uri must include a valid port", ErrInvalid)
		}
	case webCallbackPath:
		isLoopback := strings.EqualFold(parsed.Hostname(), "localhost") || parsed.Hostname() == "127.0.0.1"
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback) {
			return fmt.Errorf("%w: web redirect_uri must use HTTPS or an HTTP loopback URL", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: redirect_uri callback path is invalid", ErrInvalid)
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

func (s *AuthService) GetCurrentUser(
	ctx context.Context,
	accessToken string,
	caller CallerContext,
) (sentinel.User, error) {
	if caller.EntityType != model.EntityTypeUser || !identifier.IsValid(caller.UserID, identifier.User) {
		return sentinel.User{}, fmt.Errorf("%w: current caller is not a user", ErrForbidden)
	}
	user, err := s.sentinel.GetUser(ctx, accessToken, caller.UserID)
	if err != nil {
		return sentinel.User{}, mapSentinelResourceError(err)
	}
	if user.ID != caller.UserID || user.EntityID != caller.EntityID {
		return sentinel.User{}, fmt.Errorf("%w: Sentinel returned a mismatched current user", ErrUnavailable)
	}
	return user, nil
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
	if err := validateCallerIdentityClaims(claims); err != nil {
		return CallerContext{}, err
	}
	return CallerContext{
		EntityID:         claims.EntityID,
		EntityType:       claims.EntityType,
		UserID:           claims.UserID,
		ServiceAccountID: claims.ServiceAccountID,
		Scope:            claims.Scope,
		Groups:           claims.Groups,
	}, nil
}

func validateCallerIdentityClaims(claims sentinel.Claims) error {
	switch claims.EntityType {
	case model.EntityTypeUser:
		if !identifier.IsValid(claims.UserID, identifier.User) || claims.ServiceAccountID != "" {
			return fmt.Errorf("%w: token user identity claims are invalid", ErrUnauthorized)
		}
	case model.EntityTypeServiceAccount:
		if !identifier.IsValid(claims.ServiceAccountID, identifier.ServiceAccount) || claims.UserID != "" {
			return fmt.Errorf("%w: token service-account identity claims are invalid", ErrUnauthorized)
		}
	default:
		return fmt.Errorf("%w: token entity_type is invalid", ErrUnauthorized)
	}
	return nil
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

func mapSentinelResourceError(err error) error {
	var sentinelError *sentinel.Error
	if errors.As(err, &sentinelError) {
		switch sentinelError.Status {
		case http.StatusBadRequest:
			return fmt.Errorf("%w: %s", ErrInvalid, sentinelError.Message)
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", ErrUnauthorized, sentinelError.Message)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %s", ErrForbidden, sentinelError.Message)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrNotFound, sentinelError.Message)
		case http.StatusTooManyRequests:
			return fmt.Errorf("%w: %s", ErrUnavailable, sentinelError.Message)
		}
		if sentinelError.Status >= http.StatusInternalServerError {
			return fmt.Errorf("%w: %s", ErrUnavailable, sentinelError.Message)
		}
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
