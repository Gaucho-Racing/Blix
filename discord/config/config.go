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
	Environment, Port                                                                         string
	BotToken, GuildID                                                                         string
	RunwayURL, PublicURL, InternalToken                                                       string
	SentinelURL, SentinelClientID, SentinelServiceToken                                       string
	DatabaseHost, DatabasePort, DatabaseUser, DatabasePassword, DatabaseName, DatabaseSSLMode string
	PollInterval                                                                              time.Duration
}

func Load() (Config, error) {
	c := Config{Environment: value("ENV", "PROD"), Port: value("PORT", "9966"), BotToken: os.Getenv("DISCORD_BOT_TOKEN"), GuildID: os.Getenv("DISCORD_GUILD_ID"), RunwayURL: os.Getenv("RUNWAY_URL"), PublicURL: os.Getenv("BLIX_PUBLIC_URL"), InternalToken: os.Getenv("BLIX_INTERNAL_TOKEN"), SentinelURL: os.Getenv("SENTINEL_URL"), SentinelClientID: os.Getenv("SENTINEL_CLIENT_ID"), SentinelServiceToken: os.Getenv("SENTINEL_SERVICE_ACCOUNT_TOKEN"), DatabaseHost: os.Getenv("DATABASE_HOST"), DatabasePort: os.Getenv("DATABASE_PORT"), DatabaseUser: os.Getenv("DATABASE_USER"), DatabasePassword: os.Getenv("DATABASE_PASSWORD"), DatabaseName: os.Getenv("DATABASE_NAME"), DatabaseSSLMode: value("DATABASE_SSL_MODE", "disable")}
	port, err := strconv.ParseUint(c.Port, 10, 16)
	if err != nil || port == 0 {
		return c, fmt.Errorf("PORT must be a valid nonzero TCP port")
	}
	c.PollInterval, err = time.ParseDuration(value("DISCORD_POLL_INTERVAL", "2s"))
	if err != nil || c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return c, fmt.Errorf("DISCORD_POLL_INTERVAL must be between 100ms and 1m")
	}
	if strings.TrimSpace(c.BotToken) == "" || strings.ContainsAny(c.BotToken, " \r\n\t") || !Snowflake(c.GuildID) {
		return c, fmt.Errorf("Discord requires a raw DISCORD_BOT_TOKEN and a valid DISCORD_GUILD_ID")
	}
	for key, raw := range map[string]string{"RUNWAY_URL": c.RunwayURL, "BLIX_PUBLIC_URL": c.PublicURL, "SENTINEL_URL": c.SentinelURL} {
		if !validURL(raw) {
			return c, fmt.Errorf("%s must be an absolute HTTP URL without credentials, query, or fragment", key)
		}
	}
	for key, raw := range map[string]string{"BLIX_INTERNAL_TOKEN": c.InternalToken, "SENTINEL_CLIENT_ID": c.SentinelClientID, "SENTINEL_SERVICE_ACCOUNT_TOKEN": c.SentinelServiceToken, "DATABASE_HOST": c.DatabaseHost, "DATABASE_PORT": c.DatabasePort, "DATABASE_USER": c.DatabaseUser, "DATABASE_NAME": c.DatabaseName} {
		if strings.TrimSpace(raw) == "" {
			return c, fmt.Errorf("%s is required", key)
		}
	}
	if _, err := strconv.ParseUint(c.DatabasePort, 10, 16); err != nil {
		return c, fmt.Errorf("DATABASE_PORT must be a valid TCP port")
	}
	switch c.DatabaseSSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return c, fmt.Errorf("DATABASE_SSL_MODE is invalid")
	}
	return c, nil
}

func Snowflake(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
func (c Config) IsProduction() bool { return strings.EqualFold(c.Environment, "PROD") }
func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
