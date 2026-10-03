package config

import "testing"

func TestRequiredAndInvalidConfiguration(t *testing.T) {
	for key, value := range map[string]string{
		"PORT": "9966", "DISCORD_BOT_TOKEN": "", "DISCORD_GUILD_ID": "123",
		"DISCORD_POLL_INTERVAL": "2s",
		"RUNWAY_URL":            "http://runway:9963", "BLIX_PUBLIC_URL": "http://localhost:10310",
		"SENTINEL_URL": "http://sentinel:8080", "BLIX_INTERNAL_TOKEN": "internal-token",
		"SENTINEL_CLIENT_ID": "client", "SENTINEL_SERVICE_ACCOUNT_TOKEN": "service-token",
		"DATABASE_HOST": "db", "DATABASE_PORT": "5432", "DATABASE_USER": "postgres",
		"DATABASE_NAME": "blix", "DATABASE_SSL_MODE": "disable",
	} {
		t.Setenv(key, value)
	}
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing bot credentials")
	}
	t.Setenv("DISCORD_BOT_TOKEN", "bot-token")
	if _, err := Load(); err != nil {
		t.Fatalf("valid configuration: %v", err)
	}
	t.Setenv("DISCORD_GUILD_ID", "wrong")
	if _, err := Load(); err == nil {
		t.Fatal("accepted invalid guild ID")
	}
}
