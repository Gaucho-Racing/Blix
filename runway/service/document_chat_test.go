package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

type questionValidator struct{ err error }

func (v *questionValidator) ValidateDocuments(context.Context, []string) error      { return ErrNotFound }
func (v *questionValidator) ValidateQuestionDocument(context.Context, string) error { return v.err }

func TestDocumentChatIsPrivateScopedAndUsesOnlyQuestionTools(t *testing.T) {
	f := setup(t)
	validator := &questionValidator{}
	f.s.SetDocumentScopeValidator(validator)
	documentID := identifier.New(identifier.Prefix("document"))
	before, err := f.s.GetDocumentChat(f.ctx, documentID, f.owner.EntityID)
	if err != nil || before != nil {
		t.Fatalf("unexpected existing chat: %+v %v", before, err)
	}
	created, err := f.s.CreateDocumentChat(f.ctx, documentID, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.CreateDocumentChat(f.ctx, documentID, f.owner)
	if err != nil || again.Conversation.ID == created.Conversation.ID || again.Session.ID == created.Session.ID {
		t.Fatalf("question reused a conversation or session: %+v %v", again, err)
	}
	latest, err := f.s.GetDocumentChat(f.ctx, documentID, f.owner.EntityID)
	if err != nil || latest == nil || latest.ID != again.Conversation.ID {
		t.Fatalf("latest question was not selected: %+v %v", latest, err)
	}
	scope, err := f.s.RetrievalContext(f.ctx, created.Conversation.ID, created.Session.ID)
	if err != nil || scope.ProfileID != "document_qa_v1" || !slices.Equal(scope.DocumentIDs, []string{documentID}) {
		t.Fatalf("untrusted QA scope: %+v %v", scope, err)
	}
	var grants []model.CapabilityGrant
	if err := f.db.Where("session_id = ?", created.Session.ID).Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].ToolID != "knowledge_document_search_v1" || len(profiles[created.Session.ProfileID].Executors) != 0 {
		t.Fatalf("excessive QA capabilities: %+v", grants)
	}
	other := model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", UserID: identifier.New(identifier.User)}
	if chat, err := f.s.GetDocumentChat(f.ctx, documentID, other.EntityID); err != nil || chat != nil {
		t.Fatalf("other user's chat leaked: %+v %v", chat, err)
	}
	separate, err := f.s.CreateDocumentChat(f.ctx, documentID, other)
	if err != nil || separate.Conversation.ID == created.Conversation.ID {
		t.Fatalf("histories were shared: %+v %v", separate, err)
	}
	for _, admin := range []bool{false, true} {
		chats, err := f.s.ListConversations(f.ctx, f.owner.EntityID, admin)
		if err != nil || slices.ContainsFunc(chats, func(c model.Conversation) bool { return c.ID == created.Conversation.ID }) {
			t.Fatalf("document chat appeared in general history: %+v %v", chats, err)
		}
	}
	appended, err := f.s.AppendUserMessage(f.ctx, created.Conversation.ID, f.owner.EntityID, "What does T.3.2 require?")
	if err != nil || appended.Turn.SessionID != created.Session.ID || appended.Turn.Policy.Data().DeadlineSeconds != 300 {
		t.Fatalf("incorrect QA turn routing: %+v %v", appended, err)
	}
	if _, err := f.s.AppendUserMessage(f.ctx, created.Conversation.ID, f.owner.EntityID, "And what else?"); !errors.Is(err, ErrConflict) {
		t.Fatalf("follow-up question accepted: %v", err)
	}
	if err := f.db.Model(&model.Turn{}).Where("id = ?", appended.Turn.ID).Update("status", model.StatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AppendUserMessage(f.ctx, created.Conversation.ID, f.owner.EntityID, "After failure"); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed question allowed session reuse: %v", err)
	}
	if _, err := f.s.AppendUserMessage(f.ctx, created.Conversation.ID, other.EntityID, "Foreign question"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign write accepted: %v", err)
	}
	validator.err = ErrNotFound
	if _, err := f.s.AppendUserMessage(f.ctx, created.Conversation.ID, f.owner.EntityID, "After rejection"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected document accepted a question: %v", err)
	}
	if _, err := f.s.CreateDocumentChat(f.ctx, documentID, f.owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected document opened a chat: %v", err)
	}
	validator.err = ErrConflict
	if _, err := f.s.CreateDocumentChat(f.ctx, identifier.New(identifier.Prefix("document")), f.owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("unindexed document opened a chat: %v", err)
	}
}

func TestDocumentQuestionsCreateIndependentConversationsUnderConcurrentRequests(t *testing.T) {
	f := setup(t)
	f.s.SetDocumentScopeValidator(&questionValidator{})
	id := identifier.New(identifier.Prefix("document"))
	ids := make(chan string, 6)
	failures := make(chan error, 6)
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := f.s.CreateDocumentChat(f.ctx, id, f.owner)
			if err != nil {
				failures <- err
				return
			}
			ids <- result.Conversation.ID
		}()
	}
	group.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("concurrent questions shared a conversation")
		}
		seen[id] = true
	}
	if len(seen) != 6 {
		t.Fatalf("expected six independent questions, got %d", len(seen))
	}
}

func TestDocumentQuestionAllowsOnlyOneConcurrentSubmission(t *testing.T) {
	f := setup(t)
	f.s.SetDocumentScopeValidator(&questionValidator{})
	chat, err := f.s.CreateDocumentChat(f.ctx, identifier.New(identifier.Prefix("document")), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 6)
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := f.s.AppendUserMessage(f.ctx, chat.Conversation.ID, f.owner.EntityID, "What are the requirements?")
			results <- err
		}()
	}
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("expected one accepted question, got %d", accepted)
	}
}
