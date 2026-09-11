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
	WikiRepositoryToken  string
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
		WikiRepositoryToken:  value("BLIXWIKI_GITHUB_TOKEN", ""),
	}
	parsedRepositoryURL, err := url.ParseRequestURI(configuration.WikiRepositoryURL)
	if err != nil || parsedRepositoryURL.Scheme != "https" || parsedRepositoryURL.Host == "" {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_URL must be a valid HTTPS URL")
	}
	if configuration.WikiRepositoryBranch == "" || strings.ContainsAny(configuration.WikiRepositoryBranch, " ~^:?*[\\") {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_BRANCH is invalid")
	}
	if configuration.WikiRepositoryPath == "" {
		return Config{}, fmt.Errorf("WIKI_REPOSITORY_PATH cannot be empty")
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
