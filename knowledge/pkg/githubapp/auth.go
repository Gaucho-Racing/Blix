package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

func NewTransport(appID string, installationID string, privateKey string) (*ghinstallation.Transport, error) {
	app, err := newAppTransport(appID, privateKey)
	if err != nil {
		return nil, err
	}
	parsedInstallationID, err := strconv.ParseInt(strings.TrimSpace(installationID), 10, 64)
	if err != nil || parsedInstallationID <= 0 {
		return nil, fmt.Errorf("GITHUB_APP_INSTALLATION_ID must be a positive integer")
	}
	transport := ghinstallation.NewFromAppsTransport(app, parsedInstallationID)
	transport.Client = &http.Client{Transport: http.DefaultTransport, Timeout: 30 * time.Second}
	return transport, nil
}

func newAppTransport(appID string, privateKey string) (*ghinstallation.AppsTransport, error) {
	parsedAppID, err := strconv.ParseInt(strings.TrimSpace(appID), 10, 64)
	if err != nil || parsedAppID <= 0 {
		return nil, fmt.Errorf("GITHUB_APP_ID must be a positive integer")
	}
	privateKey = strings.ReplaceAll(strings.TrimSpace(privateKey), `\n`, "\n")
	if privateKey == "" {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY is required")
	}
	transport, err := ghinstallation.NewAppsTransport(http.DefaultTransport, parsedAppID, []byte(privateKey))
	if err != nil {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY must be a valid RSA PEM private key")
	}
	transport.Client = &http.Client{Transport: http.DefaultTransport, Timeout: 30 * time.Second}
	return transport, nil
}

func BotIdentity(ctx context.Context, appID string, privateKey string) (string, string, error) {
	transport, err := newAppTransport(appID, privateKey)
	if err != nil {
		return "", "", err
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := getJSON(ctx, &http.Client{Transport: transport, Timeout: 30 * time.Second}, "https://api.github.com/app", &app); err != nil {
		return "", "", err
	}
	if app.Slug == "" {
		return "", "", fmt.Errorf("GitHub App response has no slug")
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := getJSON(ctx, &http.Client{Timeout: 30 * time.Second}, "https://api.github.com/users/"+app.Slug+"[bot]", &user); err != nil {
		return "", "", err
	}
	if user.ID <= 0 || user.Login != app.Slug+"[bot]" {
		return "", "", fmt.Errorf("GitHub returned an invalid bot identity")
	}
	return user.Login, fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login), nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch GitHub bot identity: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch GitHub bot identity: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(output)
}
