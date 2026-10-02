package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func (s *ExecutionService) validateQuestionDocument(ctx context.Context, id string) error {
	if !identifier.IsValid(id, identifier.Prefix("document")) {
		return ErrInvalid
	}
	validator, ok := s.documents.(DocumentQuestionValidator)
	if !ok {
		return ErrUnavailable
	}
	return validator.ValidateQuestionDocument(ctx, id)
}

func documentChatQuery(tx *gorm.DB, documentID, owner string) *gorm.DB {
	ids, _ := json.Marshal([]string{documentID})
	return tx.Where("kind = ? AND caller_entity_id = ? AND document_ids = ?::jsonb AND lead_session_id IN (SELECT id FROM runway_agent_session WHERE profile_id = ?)", model.RunKindInternal, owner, string(ids), "document_qa_v1")
}

func (s *ExecutionService) GetDocumentChat(ctx context.Context, documentID, owner string) (*model.Run, error) {
	if !identifier.IsValid(documentID, identifier.Prefix("document")) || !identifier.IsValid(owner, identifier.Entity) {
		return nil, ErrInvalid
	}
	var run model.Run
	err := documentChatQuery(s.database.WithContext(ctx), documentID, owner).Order("created_at DESC, id DESC").Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *ExecutionService) CreateDocumentChat(ctx context.Context, documentID string, caller model.CallerContext) (CreatedRun, error) {
	if !identifier.IsValid(caller.EntityID, identifier.Entity) || caller.EntityType != model.EntityTypeUser || !identifier.IsValid(caller.UserID, identifier.User) {
		return CreatedRun{}, ErrForbidden
	}
	if err := s.validateQuestionDocument(ctx, documentID); err != nil {
		return CreatedRun{}, err
	}
	var result CreatedRun
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		run := model.Run{ID: identifier.New(identifier.Run), Kind: model.RunKindInternal, CallerEntityID: caller.EntityID, CallerContext: datatypes.NewJSONType(caller), Status: "active", DocumentIDs: datatypes.NewJSONSlice([]string{documentID})}
		session, err := s.createSession(tx, run.ID, nil, profiles["document_qa_v1"], "")
		if err != nil {
			return err
		}
		run.LeadSessionID = session.ID
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, model.ExecutionAttempt{RunID: run.ID, SessionID: session.ID}, "run.created_v1", run.ID, run); err != nil {
			return err
		}
		result = CreatedRun{Run: run, Session: session}
		return nil
	})
	return result, err
}

func (s *ExecutionService) validateDocumentChatMessage(ctx context.Context, id, owner string) error {
	var run model.Run
	if err := s.database.WithContext(ctx).First(&run, "id = ?", id).Error; err != nil {
		return mapDatabaseError(err)
	}
	if run.CallerEntityID != owner {
		return ErrForbidden
	}
	var session model.AgentSession
	if err := s.database.WithContext(ctx).First(&session, "id = ?", run.LeadSessionID).Error; err != nil {
		return err
	}
	if session.ProfileID != "document_qa_v1" {
		return nil
	}
	if len(run.DocumentIDs) != 1 {
		return ErrForbidden
	}
	return s.validateQuestionDocument(ctx, run.DocumentIDs[0])
}
