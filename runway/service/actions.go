package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ActionRequest struct {
	Ownership
	ToolID           string          `json:"tool_id"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Arguments        json.RawMessage `json:"arguments"`
	RequiresApproval bool            `json:"requires_approval"`
	Idempotent       bool            `json:"idempotent"`
}
type ActionResponse struct {
	Action   model.Action    `json:"action"`
	Approval *model.Approval `json:"approval,omitempty"`
	Version  uint64          `json:"version"`
}

func (s *ExecutionService) BeginAction(ctx context.Context, id string, r ActionRequest) (ActionResponse, error) {
	if r.ToolID == "" || r.IdempotencyKey == "" || len(r.IdempotencyKey) > 512 || !object(r.Arguments) {
		return ActionResponse{}, ErrInvalid
	}
	var response ActionResponse
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		var grant model.CapabilityGrant
		if err := tx.Where("session_id = ? AND tool_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", a.SessionID, r.ToolID, time.Now()).First(&grant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrForbidden
			}
			return err
		}
		var action model.Action
		err = tx.Where("target_id = ? AND idempotency_key = ?", a.TargetID, r.IdempotencyKey).First(&action).Error
		if err == nil {
			if action.ToolID != r.ToolID || !jsonEqual(action.Arguments, r.Arguments) {
				return ErrConflict
			}
			if action.Status == "completed" || action.Status == "denied" {
				response = ActionResponse{Action: action, Version: a.Version}
				return nil
			}
			if action.Status == "running" && !action.ReplaySafe {
				return fmt.Errorf("%w: the previous action has an unknown outcome; inspect it before retrying", ErrConflict)
			}
			if action.Status == "awaiting_approval" {
				var approval model.Approval
				if err := tx.First(&approval, "action_id = ?", action.ID).Error; err != nil {
					return err
				}
				if approval.Status != "approved" {
					response = ActionResponse{Action: action, Approval: &approval, Version: a.Version}
					return nil
				}
			}
			action.Status = "running"
			action.AttemptID = a.ID
			if err := tx.Save(&action).Error; err != nil {
				return err
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			action = model.Action{ReplaySafe: safeActionReplay(r.ToolID), ID: identifier.New(identifier.Action), RunID: a.RunID, TargetID: a.TargetID, SessionID: a.SessionID, AttemptID: a.ID, IdempotencyKey: r.IdempotencyKey, ToolID: r.ToolID, Arguments: datatypes.JSON(r.Arguments), Status: "running"}
			if r.RequiresApproval || r.ToolID == "knowledge_apply_v1" {
				action.Status = "awaiting_approval"
			}
			if err := tx.Create(&action).Error; err != nil {
				return err
			}
			if r.RequiresApproval || r.ToolID == "knowledge_apply_v1" {
				approval := model.Approval{ID: identifier.New(identifier.Approval), RunID: a.RunID, ActionID: action.ID, Status: "pending", RequestedBySessionID: a.SessionID}
				if err := tx.Create(&approval).Error; err != nil {
					return err
				}
				response.Approval = &approval
				if _, err := s.event(tx, a, "approval.requested_v1", approval.ID, map[string]any{"approval_id": approval.ID, "action_id": action.ID, "tool_id": action.ToolID, "arguments": json.RawMessage(action.Arguments)}); err != nil {
					return err
				}
			}
		} else {
			return err
		}
		if action.Status == "running" {
			if _, err := s.event(tx, a, "tool.call_v1", action.ID, map[string]any{"tool_id": r.ToolID, "arguments": json.RawMessage(r.Arguments)}); err != nil {
				return err
			}
		}
		a.Version++
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		response.Action = action
		response.Version = a.Version
		return nil
	})
	return response, err
}
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return string(encoded(x)) == string(encoded(y))
}
func (s *ExecutionService) FinishAction(ctx context.Context, id, actionID string, o Ownership, result json.RawMessage) (uint64, error) {
	if !object(result) {
		return 0, ErrInvalid
	}
	var v uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		var action model.Action
		if err := tx.First(&action, "id = ?", actionID).Error; err != nil {
			return err
		}
		if action.TargetID != a.TargetID || action.AttemptID != a.ID || action.Status != "running" {
			return ErrConflict
		}
		if err := s.workers.recordToolCall(tx, a.WorkerID); err != nil {
			return err
		}
		action.Status = "completed"
		action.Result = datatypes.JSON(result)
		if err := tx.Save(&action).Error; err != nil {
			return err
		}
		if err := s.usage.recordToolResult(tx, a, action, result); err != nil {
			return err
		}
		if _, err := s.event(tx, a, "tool.result_v1", action.ID, json.RawMessage(result)); err != nil {
			return err
		}
		a.Version++
		v = a.Version
		return tx.Save(&a).Error
	})
	return v, err
}
func (s *ExecutionService) WaitApproval(ctx context.Context, id, actionID string, r CheckpointRequest) error {
	return s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		var action model.Action
		if err := tx.First(&action, "id = ?", actionID).Error; err != nil {
			return err
		}
		if action.TargetID != a.TargetID || (action.Status != "awaiting_approval" && action.Status != "denied") {
			return ErrConflict
		}
		if err := s.checkpoint(tx, &a, r); err != nil {
			return err
		}
		a.Status = model.StatusSuspended
		a.LeaseExpiresAt = nil
		now := time.Now().UTC()
		a.CompletedAt = &now
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		if err := s.updateTarget(tx, a, map[string]any{"status": model.StatusWaiting}); err != nil {
			return err
		}
		var approval model.Approval
		if err := tx.First(&approval, "action_id = ?", action.ID).Error; err != nil {
			return err
		}
		if approval.Status != "pending" {
			policy, err := s.targetPolicy(tx, a)
			if err != nil {
				return err
			}
			if err := s.updateTarget(tx, a, map[string]any{"status": model.StatusQueued}); err != nil {
				return err
			}
			_, err = s.newAttempt(tx, a.RunID, a.SessionID, a.TargetID, a.TurnID, a.TaskID, policy, a.DeadlineAt)
			return err
		}
		return nil
	})
}
func (s *ExecutionService) DecideApproval(ctx context.Context, id, owner, decision, note string) error {
	if decision != "approve" && decision != "reject" {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var approval model.Approval
		if err := tx.First(&approval, "id = ?", id).Error; err != nil {
			return err
		}
		c, err := lockRun(tx, approval.RunID)
		if err != nil {
			return err
		}
		if c.CallerEntityID != owner {
			return ErrForbidden
		}
		if err := tx.First(&approval, "id = ?", id).Error; err != nil {
			return err
		}
		if approval.Status != "pending" {
			return ErrConflict
		}
		var action model.Action
		if err := tx.First(&action, "id = ?", approval.ActionID).Error; err != nil {
			return err
		}
		approval.Status = "approved"
		if decision == "reject" {
			approval.Status = "rejected"
			action.Status = "denied"
		}
		now := time.Now().UTC()
		approval.DecidedAt = &now
		approval.DecidedByEntityID = owner
		approval.DecisionMessage = note
		if err := tx.Save(&approval).Error; err != nil {
			return err
		}
		if err := tx.Save(&action).Error; err != nil {
			return err
		}
		var attempt model.ExecutionAttempt
		if err := tx.Where("target_id = ?", action.TargetID).Order("created_at DESC").First(&attempt).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, attempt, "approval.decided_v1", approval.ID, approval); err != nil {
			return err
		}
		if attempt.Status != model.StatusSuspended {
			return nil
		}
		policy, err := s.targetPolicy(tx, attempt)
		if err != nil {
			return err
		}
		if err := s.updateTarget(tx, attempt, map[string]any{"status": model.StatusQueued}); err != nil {
			return err
		}
		_, err = s.newAttempt(tx, c.ID, action.SessionID, action.TargetID, attempt.TurnID, attempt.TaskID, policy, attempt.DeadlineAt)
		return err
	})
}

func safeActionReplay(toolID string) bool {
	return contains([]string{"current_time_v1", "agent_ask_v1", "agent_execution_v1", "discord_read_v1", "discord_search_v1", "knowledge_remember_v1", "knowledge_read_v1", "knowledge_evidence_read_v1", "knowledge_source_create_v1", "knowledge_source_record_create_v1", "knowledge_apply_v1", "sentinel_api_v1", "sentinel_identity_resolve_v1", "web_extract_v1", "web_search_v1", "workbench_catalog_v1", "chart_render_v1"}, toolID)
}
