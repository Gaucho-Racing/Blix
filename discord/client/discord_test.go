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
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
			Components []json.RawMessage `json:"components"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(utf16.Encode([]rune(body.Content))) > 2000 || !body.EnforceNonce || body.Nonce != Nonce("event/run/123") || len(body.AllowedMentions.Parse) != 0 || len(body.Components) != 1 {
			t.Errorf("invalid Discord payload: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if id, err := d.Send(context.Background(), "event/run/123", "123", strings.Repeat("🙂", 2000), "https://blix.example/chat/run_123"); err != nil || id != "999" {
		t.Fatalf("send=%s err=%v", id, err)
	}
	if d.Session.Identify.Intents&discordgo.IntentMessageContent != 0 {
		t.Fatal("mention-only bot requested privileged content intent")
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
