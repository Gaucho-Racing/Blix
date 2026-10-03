package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSentinelLookupVerifiesLinkBeforeImpersonation(t *testing.T) {
	for _, linkedID := range []string{"123", "999"} {
		t.Run(linkedID, func(t *testing.T) {
			impersonated := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer service-token" {
					t.Error("missing service identity")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/core/entity/external/DISCORD/123":
					json.NewEncoder(w).Encode(map[string]any{"id": "ent_user", "type": "USER", "user": map[string]string{"id": "usr_user", "entity_id": "ent_user"}, "external_auths": []map[string]string{{"entity_id": "ent_user", "provider": "DISCORD", "external_id": linkedID}}})
				case "/api/applications/client/blix":
					w.Write([]byte(`{"id":"app_blix","client_id":"blix"}`))
				case "/api/core/token/impersonate":
					var request map[string]string
					json.NewDecoder(r.Body).Decode(&request)
					if request["user_id"] != "usr_user" || request["application_id"] != "app_blix" {
						t.Errorf("impersonation=%+v", request)
					}
					impersonated = true
					w.Write([]byte(`{"access_token":"short-lived-token","expires_in":300}`))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			s := Sentinel{HTTP: NewHTTP(server.URL, "service-token"), ClientID: "blix"}
			identity, err := s.Resolve(context.Background(), "123")
			if linkedID == "123" {
				if err != nil || identity.Token != "short-lived-token" || !impersonated {
					t.Fatalf("identity=%+v err=%v", identity, err)
				}
			} else if err == nil || impersonated {
				t.Fatal("impersonated an unrelated identity")
			}
		})
	}
}

func TestMentionVerificationNeverImpersonates(t *testing.T) {
	for _, linkedID := range []string{"123", "999", "missing"} {
		t.Run(linkedID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/core/entity/external/DISCORD/123" || r.Header.Get("Authorization") != "Bearer service-token" {
					t.Fatalf("unexpected identity request: %s", r.URL.Path)
				}
				if linkedID == "missing" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "ent_user", "type": "USER", "user": map[string]string{"id": "usr_user", "entity_id": "ent_user"}, "external_auths": []map[string]string{{"entity_id": "ent_user", "provider": "DISCORD", "external_id": linkedID}}})
			}))
			defer server.Close()
			s := Sentinel{HTTP: NewHTTP(server.URL, "service-token")}
			verified, err := s.VerifyDiscordMention(context.Background(), "123")
			if err != nil || verified != (linkedID == "123") {
				t.Fatalf("verified=%v err=%v", verified, err)
			}
		})
	}
}
