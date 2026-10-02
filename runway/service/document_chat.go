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
	return tx.Where("kind = ? AND caller_entity_id = ? AND document_ids = ?::jsonb AND lead_session_id IN (SELECT id FROM runway_agent_session WHERE profile_id = ?)", model.ConversationKindInternal, owner, string(ids), "document_qa_v1")
}

func (s *ExecutionService) GetDocumentChat(ctx context.Context, documentID, owner string) (*model.Conversation, error) {
	if !identifier.IsValid(documentID, identifier.Prefix("document")) || !identifier.IsValid(owner, identifier.Entity) {
		return nil, ErrInvalid
	}
	var conversation model.Conversation
	err := documentChatQuery(s.database.WithContext(ctx), documentID, owner).Order("created_at DESC, id DESC").Take(&conversation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (s *ExecutionService) CreateDocumentChat(ctx context.Context, documentID string, caller model.CallerContext) (CreatedConversation, error) {
	if !identifier.IsValid(caller.EntityID, identifier.Entity) || caller.EntityType != model.EntityTypeUser || !identifier.IsValid(caller.UserID, identifier.User) {
		return CreatedConversation{}, ErrForbidden
	}
	if err := s.validateQuestionDocument(ctx, documentID); err != nil {
		return CreatedConversation{}, err
	}
	var result CreatedConversation
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		conversation := model.Conversation{ID: identifier.New(identifier.Conversation), Kind: model.ConversationKindInternal, CallerEntityID: caller.EntityID, CallerContext: datatypes.NewJSONType(caller), Status: "active", DocumentIDs: datatypes.NewJSONSlice([]string{documentID})}
		session, err := s.createSession(tx, conversation.ID, nil, profiles["document_qa_v1"], "")
		if err != nil {
			return err
		}
		conversation.LeadSessionID = session.ID
		if err := tx.Create(&conversation).Error; err != nil {
			return err
		}
		if _, err := s.event(tx, model.ExecutionAttempt{ConversationID: conversation.ID, SessionID: session.ID}, "conversation.created_v1", conversation.ID, conversation); err != nil {
			return err
		}
		result = CreatedConversation{Conversation: conversation, Session: session}
		return nil
	})
	return result, err
}

func (s *ExecutionService) validateDocumentChatMessage(ctx context.Context, id, owner string) error {
	var conversation model.Conversation
	if err := s.database.WithContext(ctx).First(&conversation, "id = ?", id).Error; err != nil {
		return mapDatabaseError(err)
	}
	if conversation.CallerEntityID != owner {
		return ErrForbidden
	}
	var session model.AgentSession
	if err := s.database.WithContext(ctx).First(&session, "id = ?", conversation.LeadSessionID).Error; err != nil {
		return err
	}
	if session.ProfileID != "document_qa_v1" {
		return nil
	}
	if len(conversation.DocumentIDs) != 1 {
		return ErrForbidden
	}
	return s.validateQuestionDocument(ctx, conversation.DocumentIDs[0])
}
