package config

import "testing"

func TestConfigurationRequiresCredentialsAndBoundsConcurrency(t *testing.T) {
	t.Setenv("ENV", "DEV")
	t.Setenv("RUNWAY_URL", "http://runway:9999")
	t.Setenv("KNOWLEDGE_URL", "http://knowledge:9988")
	t.Setenv("BLIX_INTERNAL_TOKEN", "internal")
	t.Setenv("DEPOT_URL", "")
	c, err := Load()
	if err != nil || c.DocumentConcurrency != 2 || c.PageConcurrency != 4 || c.GlobalPageConcurrency != 8 {
		t.Fatalf("default configuration: %+v %v", c, err)
	}
	for _, key := range []string{"INGESTION_DOCUMENT_CONCURRENCY", "INGESTION_PAGE_CONCURRENCY", "INGESTION_GLOBAL_PAGE_CONCURRENCY"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "0")
			if _, err := Load(); err == nil {
				t.Fatal("zero concurrency accepted")
			}
		})
	}
	t.Setenv("RUNWAY_URL", "http://user:password@runway")
	if _, err := Load(); err == nil {
		t.Fatal("URL credentials accepted")
	}
	t.Setenv("RUNWAY_URL", "http://runway")
	t.Setenv("ENV", "PROD")
	if _, err := Load(); err == nil {
		t.Fatal("production without Depot accepted")
	}
}
