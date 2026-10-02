package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Environment          string
	Port                 string
	DatabaseHost         string
	DatabasePort         string
	DatabaseUser         string
	DatabasePassword     string
	DatabaseName         string
	WikiRepositoryURL    string
	WikiRepositoryBranch string
	WikiRepositoryPath   string
	DocumentStoragePath  string
	GitHubAppID          string
	GitHubInstallationID string
	GitHubPrivateKey     string
	SentinelURL          string
	SentinelClientID     string
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
	configuration := Config{
		Environment:          value("ENV", "PROD"),
		Port:                 port,
		DatabaseHost:         value("DATABASE_HOST", "localhost"),
		DatabasePort:         databasePort,
		DatabaseUser:         value("DATABASE_USER", "postgres"),
		DatabasePassword:     value("DATABASE_PASSWORD", "password"),
		DatabaseName:         value("DATABASE_NAME", "blix"),
		WikiRepositoryURL:    value("WIKI_REPOSITORY_URL", "https://github.com/gaucho-racing/blixwiki.git"),
		WikiRepositoryBranch: value("WIKI_REPOSITORY_BRANCH", "main"),
		WikiRepositoryPath:   value("WIKI_REPOSITORY_PATH", ".data/wiki"),
		DocumentStoragePath:  value("DOCUMENT_STORAGE_PATH", ".data/documents"),
		GitHubAppID:          value("GITHUB_APP_ID", ""),
		GitHubInstallationID: value("GITHUB_APP_INSTALLATION_ID", ""),
		GitHubPrivateKey:     value("GITHUB_APP_PRIVATE_KEY", ""),
		SentinelURL:          strings.TrimRight(value("SENTINEL_URL", "https://sso.gauchoracing.com"), "/"),
		SentinelClientID:     value("SENTINEL_CLIENT_ID", ""),
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
	sentinelURL, err := url.ParseRequestURI(configuration.SentinelURL)
	if err != nil || sentinelURL.Host == "" || (sentinelURL.Scheme != "http" && sentinelURL.Scheme != "https") ||
		sentinelURL.User != nil || sentinelURL.RawQuery != "" || sentinelURL.Fragment != "" {
		return Config{}, fmt.Errorf("SENTINEL_URL must be an HTTP or HTTPS URL without credentials, query, or fragment")
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
