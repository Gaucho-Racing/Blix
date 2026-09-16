package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"github.com/gaucho-racing/blix/knowledge/pkg/sentinel"
)

type AuthService struct {
	sentinel *sentinel.Client
}

type CallerContext struct {
	EntityID         string   `json:"entity_id"`
	EntityType       string   `json:"entity_type"`
	UserID           string   `json:"user_id,omitempty"`
	ServiceAccountID string   `json:"service_account_id,omitempty"`
	Groups           []string `json:"groups"`
}

func NewAuthService(client *sentinel.Client) *AuthService {
	return &AuthService{sentinel: client}
}

func (s *AuthService) Authenticate(ctx context.Context, token string) (CallerContext, error) {
	if strings.TrimSpace(token) == "" {
		return CallerContext{}, ErrUnauthorized
	}
	if !s.sentinel.Configured() {
		return CallerContext{}, fmt.Errorf("%w: Sentinel authentication is not configured", ErrUnavailable)
	}
	claims, err := s.sentinel.ValidateToken(ctx, token)
	if err != nil {
		if errors.Is(err, sentinel.ErrInvalidToken) {
			return CallerContext{}, ErrUnauthorized
		}
		return CallerContext{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !identifier.IsValid(claims.EntityID, identifier.Entity) {
		return CallerContext{}, ErrUnauthorized
	}
	switch claims.EntityType {
	case "USER":
		if !identifier.IsValid(claims.UserID, identifier.User) || claims.ServiceAccountID != "" {
			return CallerContext{}, ErrUnauthorized
		}
	case "SERVICE_ACCOUNT":
		if !identifier.IsValid(claims.ServiceAccountID, identifier.ServiceAccount) || claims.UserID != "" {
			return CallerContext{}, ErrUnauthorized
		}
	default:
		return CallerContext{}, ErrUnauthorized
	}
	return CallerContext{
		EntityID:         claims.EntityID,
		EntityType:       claims.EntityType,
		UserID:           claims.UserID,
		ServiceAccountID: claims.ServiceAccountID,
		Groups:           claims.Groups,
	}, nil
}
