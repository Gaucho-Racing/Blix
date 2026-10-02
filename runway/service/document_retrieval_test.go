package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

type documentValidator struct {
	ids []string
	err error
}

func (v *documentValidator) ValidateDocuments(_ context.Context, ids []string) error {
	v.ids = append([]string{}, ids...)
	return v.err
}

func TestConversationDocumentScopeIsValidatedAndSessionBound(t *testing.T) {
	f := setup(t)
	ids := []string{identifier.New(identifier.Prefix("document")), identifier.New(identifier.Prefix("document"))}
	validator := &documentValidator{}
	f.s.SetDocumentScopeValidator(validator)
	created, err := f.s.CreateConversationWithDocuments(f.ctx, f.owner, ids)
	if err != nil || !slices.Equal(validator.ids, ids) || !slices.Equal(created.Conversation.DocumentIDs, ids) {
		t.Fatalf("document scope was not saved: %+v %v", created, err)
	}
	scope, err := f.s.RetrievalContext(f.ctx, created.Conversation.ID, created.Session.ID)
	if err != nil || !slices.Equal(scope.DocumentIDs, ids) {
		t.Fatalf("retrieval scope: %+v %v", scope, err)
	}
	if _, err := f.s.RetrievalContext(f.ctx, created.Conversation.ID, f.conversation.Session.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign session read scope: %v", err)
	}
	validator.err = ErrNotFound
	if _, err := f.s.CreateConversationWithDocuments(f.ctx, f.owner, ids); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unverified document attached: %v", err)
	}
	oversized := make([]string, 9)
	for i := range oversized {
		oversized[i] = identifier.New(identifier.Prefix("document"))
	}
	for _, invalid := range [][]string{{ids[0], ids[0]}, {"document_invalid"}, oversized} {
		if _, err := f.s.CreateConversationWithDocuments(f.ctx, f.owner, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope accepted: %v", err)
		}
	}
}

func TestDocumentAccessVerificationFailsClosed(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{200, nil}, {404, ErrNotFound}, {403, ErrForbidden}, {409, ErrConflict}, {500, ErrUnavailable}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/documents/access" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer internal" {
					t.Error("invalid knowledge access request")
				}
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			err := NewKnowledgeDocumentClient(server.URL, "internal").ValidateDocuments(context.Background(), []string{identifier.New(identifier.Prefix("document"))})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}
