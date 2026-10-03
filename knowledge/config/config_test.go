package config

import "testing"

func TestEmbeddingConfiguration(t *testing.T) {
	t.Setenv("EMBEDDING_ENDPOINT", "")
	t.Setenv("EMBEDDING_API_KEY", "")
	t.Setenv("EMBEDDING_MODEL", "")
	t.Setenv("EMBEDDING_DIMENSIONS", "1536")
	t.Setenv("EMBEDDING_SEND_DIMENSIONS", "false")
	if cfg, err := Load(); err != nil || cfg.EmbeddingAPIKey != "" || cfg.EmbeddingDimensions != 1536 {
		t.Fatalf("disabled profile: %+v %v", cfg, err)
	}
	t.Setenv("EMBEDDING_API_KEY", "test")
	if _, err := Load(); err == nil {
		t.Fatal("partial embedding profile was accepted")
	}
	t.Setenv("EMBEDDING_ENDPOINT", "https://provider.example/v1")
	t.Setenv("EMBEDDING_MODEL", "custom-model")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBEDDING_DIMENSIONS", "768")
	t.Setenv("EMBEDDING_SEND_DIMENSIONS", "true")
	if cfg, err := Load(); err != nil || cfg.EmbeddingDimensions != 768 || !cfg.EmbeddingSendDimensions {
		t.Fatalf("custom dimensions: %+v %v", cfg, err)
	}
	for _, url := range []string{"https://user:secret@provider.example/v1", "https://provider.example/v1?key=secret", "file:///tmp/embeddings"} {
		t.Setenv("EMBEDDING_ENDPOINT", url)
		if _, err := Load(); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", url)
		}
	}
}
