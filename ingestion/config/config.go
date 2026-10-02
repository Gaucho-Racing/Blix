package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment           string
	Port                  string
	RunwayURL             string
	KnowledgeURL          string
	InternalToken         string
	DepotURL              string
	DepotBucket           string
	SentinelServiceToken  string
	DocumentConcurrency   int
	PageConcurrency       int
	GlobalPageConcurrency int
	PollInterval          time.Duration
}

func Load() (Config, error) {
	c := Config{Environment: value("ENV", "PROD"), Port: value("PORT", "9977"), RunwayURL: os.Getenv("RUNWAY_URL"), KnowledgeURL: os.Getenv("KNOWLEDGE_URL"), InternalToken: os.Getenv("BLIX_INTERNAL_TOKEN"), DepotURL: os.Getenv("DEPOT_URL"), DepotBucket: os.Getenv("DEPOT_BUCKET"), SentinelServiceToken: os.Getenv("SENTINEL_SERVICE_ACCOUNT_TOKEN")}
	port, err := strconv.ParseUint(c.Port, 10, 16)
	if err != nil || port == 0 {
		return c, fmt.Errorf("PORT must be a valid nonzero TCP port")
	}
	for key, raw := range map[string]string{"RUNWAY_URL": c.RunwayURL, "KNOWLEDGE_URL": c.KnowledgeURL} {
		if !validURL(raw) {
			return c, fmt.Errorf("%s must be an absolute HTTP URL without credentials, query, or fragment", key)
		}
	}
	if c.InternalToken == "" {
		return c, fmt.Errorf("BLIX_INTERNAL_TOKEN is required")
	}
	if c.DepotURL != "" && (!validURL(c.DepotURL) || c.DepotBucket == "" || c.SentinelServiceToken == "") {
		return c, fmt.Errorf("Depot requires a valid DEPOT_URL, DEPOT_BUCKET, and SENTINEL_SERVICE_ACCOUNT_TOKEN")
	}
	if c.IsProduction() && c.DepotURL == "" {
		return c, fmt.Errorf("DEPOT_URL is required in production")
	}
	if c.DocumentConcurrency, err = positiveInt("INGESTION_DOCUMENT_CONCURRENCY", 2, 32); err != nil {
		return c, err
	}
	if c.PageConcurrency, err = positiveInt("INGESTION_PAGE_CONCURRENCY", 4, 64); err != nil {
		return c, err
	}
	if c.GlobalPageConcurrency, err = positiveInt("INGESTION_GLOBAL_PAGE_CONCURRENCY", 8, 64); err != nil {
		return c, err
	}
	c.PollInterval, err = time.ParseDuration(value("INGESTION_POLL_INTERVAL", "2s"))
	if err != nil || c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return c, fmt.Errorf("INGESTION_POLL_INTERVAL must be between 100ms and 1m")
	}
	return c, nil
}
func (c Config) IsProduction() bool { return strings.EqualFold(c.Environment, "PROD") }
func value(key, fallback string) string {
	if result := os.Getenv(key); result != "" {
		return result
	}
	return fallback
}
func positiveInt(key string, fallback, max int) (int, error) {
	number, err := strconv.Atoi(value(key, strconv.Itoa(fallback)))
	if err != nil || number < 1 || number > max {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, max)
	}
	return number, nil
}
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
