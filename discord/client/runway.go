package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/gaucho-racing/blix/discord/config"
)

type Input struct {
	Source            string          `json:"source"`
	ExternalMessageID string          `json:"external_message_id"`
	RunID             string          `json:"run_id,omitempty"`
	Content           string          `json:"content"`
	Context           json.RawMessage `json:"context"`
	Operation         string          `json:"operation,omitempty"`
}
type InputResult struct {
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}
type Event struct {
	Position  uint64          `json:"position"`
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	TurnID    *string         `json:"turn_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}
type Runway struct{ HTTP *HTTP }

func NewRunway(c config.Config) *Runway { return &Runway{HTTP: NewHTTP(c.RunwayURL, c.InternalToken)} }
func (r *Runway) Authorize(ctx context.Context, identity Identity) error {
	var caller struct {
		EntityID   string   `json:"entity_id"`
		EntityType string   `json:"entity_type"`
		UserID     string   `json:"user_id"`
		Groups     []string `json:"groups"`
	}
	if err := r.HTTP.Request(ctx, "GET", "/v1/auth/session", nil, &caller, map[string]string{"Authorization": "Bearer " + identity.Token}); err != nil {
		return err
	}
	allowed := false
	for _, g := range caller.Groups {
		allowed = allowed || g == "BlixUsers" || g == "BlixAdmins"
	}
	if !allowed || caller.EntityType != "USER" || caller.EntityID != identity.ID || identity.User == nil || caller.UserID != identity.User.ID {
		return &Error{Status: 403, Code: "blix_access_required"}
	}
	return nil
}
func (r *Runway) Submit(ctx context.Context, input Input, token string) (InputResult, error) {
	var result InputResult
	err := r.HTTP.Request(ctx, "POST", "/internal/v1/channel-inputs", input, &result, map[string]string{"X-Blix-Caller-Token": token})
	if err == nil && (result.RunID == "" || result.SessionID == "") {
		err = &Error{Code: "invalid_runway_response"}
	}
	return result, err
}
func (r *Runway) Events(ctx context.Context, runID string, after uint64) ([]Event, error) {
	var result struct {
		Events []Event `json:"events"`
	}
	err := r.HTTP.Request(ctx, "GET", fmt.Sprintf("/internal/v1/channel-runs/%s/events?after=%d", url.PathEscape(runID), after), nil, &result, nil)
	return result.Events, err
}
