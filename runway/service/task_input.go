package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type taskQuestion struct {
	ID        string `json:"id"`
	Questions []struct {
		ID       string `json:"id"`
		Question string `json:"question"`
		Multi    bool   `json:"multi"`
		Options  []struct {
			Label string `json:"label"`
		} `json:"options"`
	} `json:"questions"`
}

type taskAnswer struct {
	ID              string   `json:"id"`
	SelectedOptions []string `json:"selected_options"`
	Text            string   `json:"text"`
}

type taskAnswerCommand struct {
	QuestionID string       `json:"question_id"`
	Answers    []taskAnswer `json:"answers"`
}

func parseTaskQuestion(payload json.RawMessage) (taskQuestion, error) {
	var question taskQuestion
	if len(payload) > 200000 || json.Unmarshal(payload, &question) != nil || strings.TrimSpace(question.ID) == "" || len(question.ID) > 200 || len(question.Questions) == 0 || len(question.Questions) > 10 {
		return question, ErrInvalid
	}
	ids := map[string]bool{}
	for _, item := range question.Questions {
		if strings.TrimSpace(item.ID) == "" || len(item.ID) > 200 || ids[item.ID] || strings.TrimSpace(item.Question) == "" || len(item.Question) > 8000 || len(item.Options) > 100 {
			return question, ErrInvalid
		}
		ids[item.ID] = true
		labels := map[string]bool{}
		for _, option := range item.Options {
			if strings.TrimSpace(option.Label) == "" || len(option.Label) > 500 || labels[option.Label] {
				return question, ErrInvalid
			}
			labels[option.Label] = true
		}
	}
	return question, nil
}

func validateTaskAnswers(question taskQuestion, answers []taskAnswer) error {
	if len(answers) != len(question.Questions) {
		return ErrInvalid
	}
	for i, item := range question.Questions {
		answer := answers[i]
		if answer.ID != item.ID || len(answer.Text) > 16000 || len(answer.SelectedOptions) > 100 || (!item.Multi && (len(answer.SelectedOptions) > 1 || (len(answer.SelectedOptions) > 0 && strings.TrimSpace(answer.Text) != ""))) || (!item.Multi && len(answer.SelectedOptions) == 0 && strings.TrimSpace(answer.Text) == "") {
			return ErrInvalid
		}
		labels := map[string]bool{}
		for _, option := range item.Options {
			labels[option.Label] = true
		}
		selected := map[string]bool{}
		for _, choice := range answer.SelectedOptions {
			if !labels[choice] || selected[choice] {
				return ErrInvalid
			}
			selected[choice] = true
		}
	}
	return nil
}

func (s *ExecutionService) requestTaskInput(tx *gorm.DB, a model.ExecutionAttempt, payload json.RawMessage) error {
	question, err := parseTaskQuestion(payload)
	if err != nil || a.TaskID == nil {
		return ErrInvalid
	}
	var session model.AgentSession
	if err := tx.First(&session, "id = ?", a.SessionID).Error; err != nil {
		return err
	}
	if session.ProfileID != "blix_code_v1" && !contains(profiles[session.ProfileID].Tools, "agent_ask_v1") {
		return ErrForbidden
	}
	var task model.Task
	if err := tx.First(&task, "id = ?", *a.TaskID).Error; err != nil {
		return err
	}
	var existing model.Event
	err = tx.Where("run_id = ? AND session_id = ? AND type = ? AND payload ->> 'id' = ?", a.RunID, a.SessionID, "runtime.question_v1", question.ID).First(&existing).Error
	if err == nil {
		previous, err := parseTaskQuestion(json.RawMessage(existing.Payload))
		if err != nil || !reflect.DeepEqual(previous, question) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if len(task.PendingInput) > 0 && string(task.PendingInput) != "null" {
		return ErrConflict
	}
	if err := tx.Model(&task).Updates(map[string]any{"status": model.StatusWaiting, "waiting_reason": "task_input", "pending_input": datatypes.JSON(payload)}).Error; err != nil {
		return err
	}
	event, err := s.event(tx, a, "runtime.question_v1", a.SessionID, json.RawMessage(payload))
	if err != nil {
		return err
	}
	var run model.Run
	if err := tx.First(&run, "id = ?", a.RunID).Error; err != nil {
		return err
	}
	policy := profiles["blix_v1"].Policy
	turn := model.Turn{
		ID: identifier.New(identifier.Turn), RunID: a.RunID, SessionID: run.LeadSessionID,
		InputPosition: event.Position, Status: model.StatusQueued, Policy: datatypes.NewJSONType(policy),
		DeadlineAt: time.Now().UTC().Add(time.Duration(policy.DeadlineSeconds) * time.Second),
		Notifications: encoded([]any{map[string]any{"type": "task.question_v1", "task_id": task.ID,
			"session_id": a.SessionID, "status": model.StatusWaiting, "question": json.RawMessage(payload)}}),
	}
	if err := tx.Create(&turn).Error; err != nil {
		return err
	}
	if err := tx.Create(&model.TurnTask{TurnID: turn.ID, TaskID: task.ID, Relation: "question"}).Error; err != nil {
		return err
	}
	_, err = s.newAttempt(tx, turn.RunID, turn.SessionID, turn.ID, &turn.ID, nil, policy, turn.DeadlineAt)
	return err
}

func (s *ExecutionService) answerTaskInput(tx *gorm.DB, task model.Task, r TaskControlRequest) error {
	var answers []taskAnswer
	if strings.TrimSpace(r.QuestionID) == "" || len(r.QuestionID) > 200 || len(r.Answers) > 200000 || json.Unmarshal(r.Answers, &answers) != nil || len(answers) == 0 || len(answers) > 10 {
		return ErrInvalid
	}
	for i := range answers {
		answers[i].Text = strings.TrimSpace(answers[i].Text)
		if answers[i].SelectedOptions == nil {
			answers[i].SelectedOptions = []string{}
		}
	}
	payload := taskAnswerCommand{QuestionID: r.QuestionID, Answers: answers}
	var prior model.SessionCommand
	err := tx.Where("target_id = ? AND kind = ? AND payload ->> 'question_id' = ?", task.ID, "answer", r.QuestionID).First(&prior).Error
	if err == nil {
		var previous taskAnswerCommand
		if json.Unmarshal(prior.Payload, &previous) != nil || !reflect.DeepEqual(previous, payload) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	question, err := parseTaskQuestion(json.RawMessage(task.PendingInput))
	if err != nil || terminal(task.Status) || question.ID != r.QuestionID {
		return ErrConflict
	}
	if err := validateTaskAnswers(question, answers); err != nil {
		return err
	}
	command := model.SessionCommand{ID: identifier.New(identifier.Command), RequestRef: r.RequestRef,
		SessionID: task.SessionID, TargetID: task.ID, Kind: "answer", Payload: encoded(payload)}
	if err := tx.Create(&command).Error; err != nil {
		return err
	}
	var latest model.ExecutionAttempt
	if err := tx.Where("target_id = ?", task.ID).Order("number DESC").First(&latest).Error; err != nil {
		return err
	}
	status := model.StatusRunning
	if latest.Status == model.StatusSuspended {
		status = model.StatusQueued
		if _, err := s.newAttempt(tx, task.RunID, task.SessionID, task.ID, nil, &task.ID, task.Policy.Data(), task.DeadlineAt); err != nil {
			return err
		}
	}
	if err := tx.Model(&task).Updates(map[string]any{"status": status, "waiting_reason": "", "pending_input": nil}).Error; err != nil {
		return err
	}
	_, err = s.event(tx, model.ExecutionAttempt{RunID: task.RunID, SessionID: task.SessionID, TaskID: &task.ID}, "task.answered_v1", task.ID, payload)
	return err
}

func (s *ExecutionService) AcknowledgeCommands(ctx context.Context, id string, o Ownership, ids []string) (uint64, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return 0, ErrInvalid
	}
	seen := map[string]bool{}
	for _, commandID := range ids {
		if !identifier.IsValid(commandID, identifier.Command) || seen[commandID] {
			return 0, ErrInvalid
		}
		seen[commandID] = true
	}
	var version uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, o)
		if err != nil {
			return err
		}
		if err := acknowledgeSessionCommands(tx, a.SessionID, a.TargetID, ids); err != nil {
			return err
		}
		a.Version++
		version = a.Version
		return tx.Save(&a).Error
	})
	return version, err
}

func acknowledgeSessionCommands(tx *gorm.DB, sessionID, targetID string, ids []string) error {
	var count int64
	if err := tx.Model(&model.SessionCommand{}).Where("id IN ? AND session_id = ? AND target_id = ?", ids, sessionID, targetID).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrForbidden
	}
	return tx.Model(&model.SessionCommand{}).Where("id IN ? AND session_id = ? AND target_id = ? AND acknowledged_at IS NULL", ids, sessionID, targetID).Update("acknowledged_at", time.Now().UTC()).Error
}

func taskAnswerDelivery(tx *gorm.DB, taskID string) (map[string]any, error) {
	var command model.SessionCommand
	err := tx.Where("target_id = ? AND kind = ?", taskID, "answer").Order("created_at DESC").First(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var answer taskAnswerCommand
	if err := json.Unmarshal(command.Payload, &answer); err != nil {
		return nil, err
	}
	status := "queued"
	if command.AcknowledgedAt != nil {
		status = "delivered_to_agent"
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", command.SessionID).Error; err != nil {
			return nil, err
		}
		if session.ProfileID == "blix_code_v1" {
			status = "delivered_to_sandbox"
		}
	}
	return map[string]any{"question_id": answer.QuestionID, "status": status}, nil
}

type TaskInputRequest struct {
	CheckpointRequest
	Question json.RawMessage `json:"question"`
}

func (s *ExecutionService) WaitTaskInput(ctx context.Context, id string, r TaskInputRequest) (uint64, error) {
	var version uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		a, err := s.owned(tx, id, r.Ownership)
		if err != nil {
			return err
		}
		var session model.AgentSession
		if err := tx.First(&session, "id = ?", a.SessionID).Error; err != nil {
			return err
		}
		if a.TaskID == nil || !contains(profiles[session.ProfileID].Tools, "agent_ask_v1") {
			return ErrForbidden
		}
		if err := s.checkpoint(tx, &a, r.CheckpointRequest); err != nil {
			return err
		}
		if err := s.requestTaskInput(tx, a, r.Question); err != nil {
			return err
		}
		a.Status = model.StatusSuspended
		a.LeaseExpiresAt = nil
		now := time.Now().UTC()
		a.CompletedAt = &now
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, a, "attempt.suspended_v1", a.ID, map[string]any{"waiting_reason": "task_input"}); err != nil {
			return err
		}
		version = a.Version
		return nil
	})
	return version, err
}
