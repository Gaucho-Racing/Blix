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

func TestComponentsV2DeliveryDisablesMentionsAndUsesNonce(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot test-token" {
			t.Error("missing bot authorization")
		}
		var body struct {
			Content         string                 `json:"content"`
			Flags           discordgo.MessageFlags `json:"flags"`
			Nonce           string                 `json:"nonce"`
			EnforceNonce    bool                   `json:"enforce_nonce"`
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
		if body.Content != "" || body.Flags&discordgo.MessageFlagsIsComponentsV2 == 0 || !body.EnforceNonce || body.Nonce != Nonce("event/run/123") || len(body.AllowedMentions.Parse) != 0 || len(body.Components) != 3 {
			t.Errorf("invalid Discord payload: %+v", body)
		}
		if len(body.Components) == 3 {
			var text, footer discordgo.TextDisplay
			var separator discordgo.Separator
			if err := json.Unmarshal(body.Components[0], &text); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body.Components[1], &separator); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body.Components[2], &footer); err != nil {
				t.Fatal(err)
			}
			if text.Content == "" || len(utf16.Encode([]rune(text.Content))) > 1801 || separator.Divider == nil || !*separator.Divider || separator.Spacing == nil || *separator.Spacing != discordgo.SeparatorSpacingSizeSmall || footer.Content != "-# [View in Blix](https://blix.example/chat/run_123)" {
				t.Errorf("invalid V2 layout: text=%+v separator=%+v footer=%+v", text, separator, footer)
			}
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
	if id, err := d.Send(context.Background(), "event/run/123", "123", "456", strings.Repeat("🙂", 2000), "https://blix.example/chat/run_123", "", nil); err != nil || id != "999" {
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
	if _, err := d.Send(context.Background(), "mentions", "300", "500", "Ask <@123> or <@!123>. <@999> @everyone <@&456>", "", "", []string{"123", "123", "555"}); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesFooterLinksToCitedAnswer(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Components []json.RawMessage `json:"components"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Components) != 3 {
			t.Fatalf("unexpected components: %s", body.Components)
		}
		var footer discordgo.TextDisplay
		if err := json.Unmarshal(body.Components[2], &footer); err != nil {
			t.Fatal(err)
		}
		if footer.Content != "-# [Sources](https://blix.example/chat/run_123#message-msg_123) · [View in Blix](https://blix.example/chat/run_123)" {
			t.Errorf("unexpected footer: %s", footer.Content)
		}
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if _, err := d.Send(context.Background(), "sources", "300", "500", "Mapache is our telemetry platform.", "https://blix.example/chat/run_123", "https://blix.example/chat/run_123#message-msg_123", nil); err != nil {
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

func TestDeletePlaceholderHandlesAlreadyDeletedMessage(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			d, err := NewDiscord("test-token")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v9/channels/300/messages/901" {
					t.Errorf("unexpected cleanup request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			d.Session.Client = server.Client()
			d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
			err = d.Delete(context.Background(), "300", "901")
			if (err == nil) != (status == http.StatusNoContent || status == http.StatusNotFound) {
				t.Fatalf("status=%d error=%v", status, err)
			}
		})
	}
}

func TestV2MessageWithoutRunHasNoFooter(t *testing.T) {
	d, err := NewDiscord("test-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Components []json.RawMessage `json:"components"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Components) != 1 {
			t.Errorf("unexpected components: %s", body.Components)
		}
		w.Write([]byte(`{"id":"999"}`))
	}))
	defer server.Close()
	d.Session.Client = server.Client()
	d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
	if _, err := d.Send(context.Background(), "greeting", "300", "500", "Hey! How can I help?", "", "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveEyesOnlyRemovesBotsOwnReaction(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			d, err := NewDiscord("test-token")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/channels/300/messages/500/reactions/👀/@me") {
					t.Errorf("unexpected reaction removal: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			d.Session.Client = server.Client()
			d.Session.Client.Transport = rewriteTransport{base: server.URL, inner: http.DefaultTransport}
			err = d.RemoveReaction(context.Background(), "300", "500", "👀")
			if (err == nil) != (status != http.StatusForbidden) {
				t.Fatalf("status=%d error=%v", status, err)
			}
		})
	}
}
