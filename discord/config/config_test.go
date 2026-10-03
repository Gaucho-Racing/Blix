package config

import "testing"

func TestDisabledAndInvalidConfiguration(t *testing.T) {
	t.Setenv("DISCORD_ENABLED", "false")
	t.Setenv("PORT", "9966")
	c, err := Load()
	if err != nil || c.Enabled {
		t.Fatalf("disabled config=%+v err=%v", c, err)
	}
	t.Setenv("DISCORD_ENABLED", "true")
	if _, err = Load(); err == nil {
		t.Fatal("enabled without credentials")
	}
	t.Setenv("DISCORD_ENABLED", "false")
	t.Setenv("DISCORD_CHANNEL_IDS", "123,wrong")
	if _, err = Load(); err == nil {
		t.Fatal("accepted invalid channel IDs")
	}
}
