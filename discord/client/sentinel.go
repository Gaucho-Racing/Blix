package client

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gaucho-racing/blix/discord/config"
)

type Identity struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	User *struct {
		ID       string `json:"id"`
		EntityID string `json:"entity_id"`
	} `json:"user"`
	ExternalAuths []struct {
		EntityID   string `json:"entity_id"`
		Provider   string `json:"provider"`
		ExternalID string `json:"external_id"`
	} `json:"external_auths"`
	Token string `json:"-"`
}

type Sentinel struct {
	HTTP     *HTTP
	ClientID string
}

func NewSentinel(c config.Config) *Sentinel {
	return &Sentinel{HTTP: NewHTTP(c.SentinelURL, c.SentinelServiceToken), ClientID: c.SentinelClientID}
}
func (s *Sentinel) Resolve(ctx context.Context, discordID string) (Identity, error) {
	var identity Identity
	if err := s.HTTP.Request(ctx, "GET", "/api/core/entity/external/DISCORD/"+url.PathEscape(discordID), nil, &identity, nil); err != nil {
		return identity, err
	}
	if identity.Type != "USER" || identity.User == nil || identity.User.EntityID != identity.ID || !strings.HasPrefix(identity.ID, "ent_") || !strings.HasPrefix(identity.User.ID, "usr_") {
		return identity, &Error{Status: 403, Code: "invalid_linked_identity"}
	}
	matched := false
	for _, link := range identity.ExternalAuths {
		if link.EntityID != identity.ID {
			return identity, &Error{Status: 403, Code: "invalid_linked_identity"}
		}
		matched = matched || (strings.EqualFold(link.Provider, "DISCORD") && link.ExternalID == discordID)
	}
	if !matched {
		return identity, &Error{Status: 403, Code: "invalid_linked_identity"}
	}
	var app struct {
		ID       string `json:"id"`
		ClientID string `json:"client_id"`
	}
	if err := s.HTTP.Request(ctx, "GET", "/api/applications/client/"+url.PathEscape(s.ClientID), nil, &app, nil); err != nil {
		return identity, err
	}
	if app.ID == "" || app.ClientID != s.ClientID {
		return identity, &Error{Code: "invalid_application_response"}
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := s.HTTP.Request(ctx, "POST", "/api/core/token/impersonate", map[string]string{"user_id": identity.User.ID, "application_id": app.ID}, &token, nil); err != nil {
		return identity, err
	}
	if token.AccessToken == "" || token.ExpiresIn <= 0 {
		return identity, fmt.Errorf("invalid Sentinel impersonation response")
	}
	identity.Token = token.AccessToken
	return identity, nil
}
