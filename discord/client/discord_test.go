package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/bwmarrin/discordgo"
)

func TestConciseDeliveryDisablesMentionsAndUsesNonce(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot test-token" {
			t.Error("missing bot authorization")
		}
		var body struct {
			Content         string `json:"content"`
			Nonce           string `json:"nonce"`
			EnforceNonce    bool   `json:"enforce_nonce"`
			AllowedMentions struct {
				Parse       []string `json:"parse"`
				RepliedUser bool     `json:"replied_user"`
			} `json:"allowed_mentions"`
			Reference  *discordgo.MessageReference `json:"message_reference"`
			Components []json.RawMessage           `json:"components"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(utf16.Encode([]rune(body.Content))) > 2000 || !body.EnforceNonce || body.Nonce != Nonce("event/run/123") || len(body.AllowedMentions.Parse) != 0 || len(body.Components) != 1 {
			t.Errorf("invalid Discord payload: %+v", body)
		}
		if body.Reference == nil || body.Reference.MessageID != "456" || body.Reference.ChannelID != "123" || body.Reference.FailIfNotExists == nil || *body.Reference.FailIfNotExists || body.AllowedMentions.RepliedUser {
			t.Errorf("invalid reply reference: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if id, err := d.Send(context.Background(), "event/run/123", "123", "456", strings.Repeat("🙂", 2000), "https://blix.example/chat/run_123", nil); err != nil || id != "999" {
		t.Fatalf("send=%s err=%v", id, err)
	}
	if d.Session.Identify.Intents&discordgo.IntentMessageContent != 0 {
		t.Fatal("mention-only bot requested privileged content intent")
	}
}

func TestExplicitUserMentionAllowlist(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AllowedMentions discordgo.MessageAllowedMentions `json:"allowed_mentions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mentions := body.AllowedMentions
		if len(mentions.Users) != 1 || mentions.Users[0] != "123" || len(mentions.Parse) != 0 || len(mentions.Roles) != 0 || mentions.RepliedUser {
			t.Errorf("invalid allowlist: %+v", mentions)
		}
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if _, err := d.Send(context.Background(), "mentions", "300", "500", "Ask <@123> or <@!123>. <@999> @everyone <@&456>", "", []string{"123", "123", "555"}); err != nil {
		t.Fatal(err)
	}
}

func TestEyesReactionUsesDiscordReactionEndpoint(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/channels/300/messages/500/reactions/👀/@me") || r.Header.Get("Authorization") != "Bot test-token" {
			t.Errorf("unexpected reaction request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if err := d.React(context.Background(), "300", "500", "👀"); err != nil {
		t.Fatal(err)
	}
}

type rewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	serverReq, _ := http.NewRequest(r.Method, t.base+r.URL.Path, nil)
	req.URL = serverReq.URL
	req.Host = req.URL.Host
	return t.inner.RoundTrip(req)
}
