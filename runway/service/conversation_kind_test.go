package service

import (
	"fmt"
	"slices"
	"testing"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

func TestConversationHistoryExcludesInternalDocumentRuns(t *testing.T) {
	f := setup(t)
	f.s.SetDocumentScopeValidator(&questionValidator{})
	if f.conversation.Conversation.Kind != model.ConversationKindChat {
		t.Fatalf("normal conversation has incorrect kind: %+v", f.conversation.Conversation)
	}
	documentID := identifier.New(identifier.Prefix("document"))
	ingestion, err := f.s.StartDocumentIngestion(f.ctx, DocumentIngestionRequest{
		Key: fmt.Sprintf("%s:1", documentID), Caller: f.owner, DocumentID: documentID,
		Generation: 1, Objective: "Read this document and draft wiki changes.",
	})
	if err != nil {
		t.Fatal(err)
	}
	question, err := f.s.CreateDocumentChat(f.ctx, documentID, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := f.s.CreateConversation(f.ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Conversation{}).Where("id = ?", internal.Conversation.ID).Update("kind", model.ConversationKindInternal).Error; err != nil {
		t.Fatal(err)
	}
	other := model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: model.EntityTypeUser, UserID: identifier.New(identifier.User)}
	otherChat, err := f.s.CreateConversation(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ingestion.ConversationID, question.Conversation.ID, internal.Conversation.ID} {
		if err := f.s.AuthorizeConversationAccess(f.ctx, id, f.owner.EntityID, false); err != nil {
			t.Fatalf("internal audit access lost: %v", err)
		}
		details, err := f.s.GetConversation(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if details["conversation"].(model.Conversation).Kind != model.ConversationKindInternal {
			t.Fatalf("internal kind was not persisted for %s", id)
		}
	}
	for _, status := range []string{"active", "completed", "cancelled"} {
		if err := f.db.Model(&model.Conversation{}).Where("id IN ?", []string{ingestion.ConversationID, question.Conversation.ID}).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		for _, admin := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/admin=%t", status, admin), func(t *testing.T) {
				conversations, err := f.s.ListConversations(f.ctx, f.owner.EntityID, admin)
				if err != nil {
					t.Fatal(err)
				}
				ids := make([]string, 0, len(conversations))
				for _, conversation := range conversations {
					if conversation.Kind != model.ConversationKindChat {
						t.Fatalf("internal conversation leaked: %+v", conversation)
					}
					ids = append(ids, conversation.ID)
				}
				want := []string{f.conversation.Conversation.ID}
				if admin {
					want = append(want, otherChat.Conversation.ID)
				}
				slices.Sort(ids)
				slices.Sort(want)
				if !slices.Equal(ids, want) {
					t.Fatalf("history = %v; want %v", ids, want)
				}
			})
		}
	}
}

func TestConversationKindRejectsInvalidValues(t *testing.T) {
	f := setup(t)
	if err := f.db.Model(&model.Conversation{}).Where("id = ?", f.conversation.Conversation.ID).Update("kind", "invalid").Error; err == nil {
		t.Fatal("invalid conversation kind was accepted")
	}
}
