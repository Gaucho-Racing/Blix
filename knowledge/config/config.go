package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	EmbeddingEndpoint           string
	EmbeddingAPIKey             string
	EmbeddingModel              string
	EmbeddingRevision           string
	EmbeddingDimensions         int
	EmbeddingSendDimensions     bool
	DepotURL                    string
	DepotBucket                 string
	SentinelServiceAccountToken string
	RunwayURL                   string
	Environment                 string
	Port                        string
	DatabaseHost                string
	DatabasePort                string
	DatabaseUser                string
	DatabasePassword            string
	DatabaseName                string
	DatabaseSSLMode             string
	WikiRepositoryURL           string
	WikiRepositoryBranch        string
	WikiRepositoryPath          string
	DocumentStoragePath         string
	GitHubAppID                 string
	GitHubInstallationID        string
	GitHubPrivateKey            string
	SentinelURL                 string
	SentinelClientID            string
}

func Load() (Config, error) {
	port := value("PORT", "9988")
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return Config{}, fmt.Errorf("PORT must be a valid TCP port")
	}
	databasePort := value("DATABASE_PORT", "5432")
	if _, err := strconv.ParseUint(databasePort, 10, 16); err != nil {
		return Config{}, fmt.Errorf("DATABASE_PORT must be a valid TCP port")
	}
	dimensions, err := strconv.Atoi(value("EMBEDDING_DIMENSIONS", "1536"))
	if err != nil || dimensions < 1 || dimensions > 4096 {
		return Config{}, fmt.Errorf("EMBEDDING_DIMENSIONS must be between 1 and 4096")
	}
	sendDimensions, err := strconv.ParseBool(value("EMBEDDING_SEND_DIMENSIONS", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("EMBEDDING_SEND_DIMENSIONS must be a boolean")
	}
	configuration := Config{
		EmbeddingEndpoint:           strings.TrimRight(value("EMBEDDING_ENDPOINT", ""), "/"),
		EmbeddingAPIKey:             value("EMBEDDING_API_KEY", ""),
		EmbeddingModel:              value("EMBEDDING_MODEL", ""),
		EmbeddingRevision:           value("EMBEDDING_REVISION", "1"),
		EmbeddingDimensions:         dimensions,
		EmbeddingSendDimensions:     sendDimensions,
		DepotURL:                    strings.TrimRight(value("DEPOT_URL", ""), "/"),
		DepotBucket:                 value("DEPOT_BUCKET", "blix-documents"),
		SentinelServiceAccountToken: value("SENTINEL_SERVICE_ACCOUNT_TOKEN", ""),
		RunwayURL:                   strings.TrimRight(value("RUNWAY_URL", "http://runway:9999"), "/"),
		Environment:                 value("ENV", "PROD"),
		Port:                        port,
		DatabaseHost:                value("DATABASE_HOST", "localhost"),
		DatabasePort:                databasePort,
		DatabaseUser:                value("DATABASE_USER", "postgres"),
		DatabasePassword:            value("DATABASE_PASSWORD", "password"),
		DatabaseName:                value("DATABASE_NAME", "blix"),
		DatabaseSSLMode:             value("DATABASE_SSL_MODE", "disable"),
		WikiRepositoryURL:           value("WIKI_REPOSITORY_URL", "https://github.com/gaucho-racing/blixwiki.git"),
		WikiRepositoryBranch:        value("WIKI_REPOSITORY_BRANCH", "main"),
		WikiRepositoryPath:          value("WIKI_REPOSITORY_PATH", ".data/wiki"),
		DocumentStoragePath:         value("DOCUMENT_STORAGE_PATH", ".data/documents"),
		GitHubAppID:                 value("GITHUB_APP_ID", ""),
		GitHubInstallationID:        value("GITHUB_APP_INSTALLATION_ID", ""),
		GitHubPrivateKey:            value("GITHUB_APP_PRIVATE_KEY", ""),
		SentinelURL:                 strings.TrimRight(value("SENTINEL_URL", "https://sso.gauchoracing.com"), "/"),
		SentinelClientID:            value("SENTINEL_CLIENT_ID", ""),
	}
	for _, c := range configuration.EmbeddingAPIKey {
		if c < 33 || c > 126 {
			return Config{}, fmt.Errorf("EMBEDDING_API_KEY must contain only printable non-whitespace ASCII characters")
		}
	}
	if configuration.EmbeddingAPIKey != "" && (configuration.EmbeddingEndpoint == "" || configuration.EmbeddingModel == "") {
		return Config{}, fmt.Errorf("EMBEDDING_ENDPOINT and EMBEDDING_MODEL are required when EMBEDDING_API_KEY is set")
	}
	parsedRepositoryURL, err := url.ParseRequestURI(configuration.WikiRepositoryURL)
	if err != nil || parsedRepositoryURL.Scheme != "https" || parsedRepositoryURL.Host != "github.com" ||
		parsedRepositoryURL.User != nil || parsedRepositoryURL.RawQuery != "" || parsedRepositoryURL.Fragment != "" {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_URL must be an HTTPS github.com URL without credentials, query, or fragment")
	}
	if configuration.WikiRepositoryBranch == "" || strings.ContainsAny(configuration.WikiRepositoryBranch, " ~^:?*[\\") {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_BRANCH is invalid")
	}
	if configuration.WikiRepositoryPath == "" {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_PATH cannot be empty")
	}
	switch configuration.DatabaseSSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return Config{}, fmt.Errorf("DATABASE_SSL_MODE must be disable, require, verify-ca, or verify-full")
	}
	sentinelURL, err := url.ParseRequestURI(configuration.SentinelURL)
	if err != nil || sentinelURL.Host == "" || (sentinelURL.Scheme != "http" && sentinelURL.Scheme != "https") ||
		sentinelURL.User != nil || sentinelURL.RawQuery != "" || sentinelURL.Fragment != "" {
		return Config{}, fmt.Errorf("SENTINEL_URL must be an HTTP or HTTPS URL without credentials, query, or fragment")
	}
	for name, raw := range map[string]string{"EMBEDDING_ENDPOINT": configuration.EmbeddingEndpoint, "DEPOT_URL": configuration.DepotURL, "RUNWAY_URL": configuration.RunwayURL} {
		if raw == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("%s must be an HTTP or HTTPS URL without credentials, query, or fragment", name)
		}
	}
	return configuration, nil
}

func (c Config) IsProduction() bool {
	return c.Environment == "PROD"
}

func value(name string, fallback string) string {
	if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
		return configured
	}
	return fallback
}
