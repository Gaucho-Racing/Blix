package embedding

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseValidationAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"reordered", `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`, true},
		{"duplicate", `{"data":[{"index":0,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`, false},
		{"missing", `{"data":[{"index":0,"embedding":[1,0]}]}`, false},
		{"wrong_dimensions", `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[1]}]}`, false},
		{"zero", `{"data":[{"index":0,"embedding":[0,0]},{"index":1,"embedding":[1,0]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer test" {
					t.Error("invalid request")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			vectors, err := New(server.URL, "test", "model", 2).Embed(context.Background(), []string{"first", "second"})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && vectors[0][0] != 1 {
				t.Fatal("provider response order was trusted instead of indexes")
			}
		})
	}
}
func TestProviderErrorDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, "private provider details")
	}))
	defer server.Close()
	_, err := New(server.URL, "test", "model", 2).Embed(context.Background(), []string{"first"})
	if err == nil || err.Error() != "embedding provider returned HTTP 429" {
		t.Fatal(err)
	}
}

func TestNamespaceChangesWithModelRevisionAndDimensions(t *testing.T) {
	client := New("https://provider.example/v1", "first-key", "model", 1536)
	original := client.Namespace("1")
	client.APIKey = "rotated-key"
	if client.Namespace("1") != original {
		t.Fatal("rotating credentials discarded reusable vectors")
	}
	if client.Namespace("2") == original {
		t.Fatal("model revision reused incompatible vectors")
	}
	client.Dimensions = 768
	if client.Namespace("1") == original {
		t.Fatal("dimension change reused incompatible vectors")
	}
}
