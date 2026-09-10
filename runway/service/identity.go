package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"github.com/gaucho-racing/blix/runway/pkg/sentinel"
)

const maxIdentityIDs = 100

type IdentityService struct {
	sentinel            *sentinel.Client
	serviceAccountToken string
}

func NewIdentityService(client *sentinel.Client, serviceAccountToken string) *IdentityService {
	return &IdentityService{
		sentinel:            client,
		serviceAccountToken: strings.TrimSpace(serviceAccountToken),
	}
}

func (s *IdentityService) Resolve(
	ctx context.Context,
	entityIDs []string,
) ([]sentinel.IdentitySummary, error) {
	if len(entityIDs) > maxIdentityIDs {
		return nil, fmt.Errorf("%w: at most %d entity IDs may be resolved at once", ErrInvalid, maxIdentityIDs)
	}
	unique := make([]string, 0, len(entityIDs))
	seen := make(map[string]struct{}, len(entityIDs))
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if !identifier.IsValid(entityID, identifier.Entity) {
			return nil, fmt.Errorf("%w: entity IDs must be prefixed ULIDs", ErrInvalid)
		}
		if _, exists := seen[entityID]; exists {
			continue
		}
		seen[entityID] = struct{}{}
		unique = append(unique, entityID)
	}
	if len(unique) == 0 {
		return []sentinel.IdentitySummary{}, nil
	}
	if s.serviceAccountToken == "" {
		return nil, fmt.Errorf("%w: Sentinel identity resolution is not configured", ErrUnavailable)
	}
	identities, err := s.sentinel.ResolveIdentities(ctx, s.serviceAccountToken, unique)
	if err != nil {
		return nil, mapSentinelResourceError(err)
	}
	return identities, nil
}
