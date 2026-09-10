package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type authConfiguration struct {
	AuthorizationURL string `json:"authorization_url"`
	RedirectURI      string `json:"redirect_uri"`
}

type tokenSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type callerContext struct {
	EntityID string   `json:"entity_id"`
	UserID   string   `json:"user_id"`
	Scope    string   `json:"scope"`
	Groups   []string `json:"groups"`
}

type callbackResult struct {
	code string
	err  error
}

func (c *client) login(ctx context.Context) (callerContext, error) {
	state, err := randomState()
	if err != nil {
		return callerContext{}, err
	}
	var configuration authConfiguration
	if err := c.requestWithoutAuth(
		ctx,
		http.MethodGet,
		"/v1/auth/config?state="+url.QueryEscape(state),
		nil,
		&configuration,
	); err != nil {
		return callerContext{}, err
	}
	code, err := receiveAuthorizationCode(ctx, configuration, state)
	if err != nil {
		return callerContext{}, err
	}
	var session tokenSession
	if err := c.requestWithoutAuth(
		ctx,
		http.MethodPost,
		"/v1/auth/login",
		map[string]string{"code": code},
		&session,
	); err != nil {
		return callerContext{}, err
	}
	c.session = &session
	caller, err := c.getSession(ctx)
	if err != nil {
		c.session = nil
		return callerContext{}, err
	}
	c.caller = &caller
	if err := saveSession(session); err != nil {
		c.session = nil
		c.caller = nil
		return callerContext{}, fmt.Errorf("save CLI session: %w", err)
	}
	return caller, nil
}

func (c *client) getSession(ctx context.Context) (callerContext, error) {
	var caller callerContext
	if err := c.request(ctx, http.MethodGet, "/v1/auth/session", nil, &caller); err != nil {
		return callerContext{}, err
	}
	return caller, nil
}

func (c *client) refreshSession(ctx context.Context) error {
	if c.session == nil || c.session.RefreshToken == "" {
		return errors.New("no refresh token is available")
	}
	var refreshed tokenSession
	if err := c.requestWithoutAuth(
		ctx,
		http.MethodPost,
		"/v1/auth/refresh",
		map[string]string{"refresh_token": c.session.RefreshToken},
		&refreshed,
	); err != nil {
		return err
	}
	c.session = &refreshed
	return saveSession(refreshed)
}

func (c *client) logout(ctx context.Context) {
	_ = c.requestWithoutAuth(ctx, http.MethodPost, "/v1/auth/logout", nil, nil)
	c.session = nil
	c.caller = nil
	_ = removeSession()
}

func receiveAuthorizationCode(ctx context.Context, configuration authConfiguration, expectedState string) (string, error) {
	redirect, err := url.Parse(configuration.RedirectURI)
	if err != nil {
		return "", fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	if redirect.Scheme != "http" || !isLoopbackHost(redirect.Hostname()) || redirect.Port() == "" {
		return "", errors.New("Sentinel redirect URI must be an HTTP loopback URL with an explicit port")
	}
	callbackPath := redirect.Path
	if callbackPath == "" {
		callbackPath = "/"
	}
	listener, err := net.Listen("tcp", "0.0.0.0:"+redirect.Port())
	if err != nil {
		return "", fmt.Errorf("listen for Sentinel callback: %w", err)
	}
	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("state") != expectedState {
			http.Error(writer, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		if oauthError := query.Get("error"); oauthError != "" {
			description := query.Get("error_description")
			if description == "" {
				description = oauthError
			}
			select {
			case results <- callbackResult{err: errors.New(description)}:
			default:
			}
			writeCallbackPage(writer, "Sentinel login failed", description, false)
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(writer, "Authorization code is missing", http.StatusBadRequest)
			return
		}
		select {
		case results <- callbackResult{code: code}:
		default:
		}
		writeCallbackPage(writer, "You’re signed in", "Return to the Blix CLI to continue.", true)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	fmt.Println()
	fmt.Println(pinkStyle.Render("Sentinel login"))
	fmt.Println(mutedStyle.Render("Open this URL to continue:"))
	fmt.Println(configuration.AuthorizationURL)
	_ = openBrowser(configuration.AuthorizationURL)

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return "", context.Canceled
		}
		return "", fmt.Errorf("serve Sentinel callback: %w", err)
	case result := <-results:
		return result.code, result.err
	case <-time.After(5 * time.Minute):
		return "", errors.New("Sentinel login timed out")
	}
}

func writeCallbackPage(writer http.ResponseWriter, title string, message string, success bool) {
	color := "#E105A3"
	if !success {
		color = "#D64545"
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(writer, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head><body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#faf7fb;color:#241f25;font:16px system-ui"><main style="width:min(420px,calc(100%% - 48px));padding:32px;border:1px solid #e8dfe8;border-radius:18px;background:white;box-shadow:0 18px 50px #6a31501c"><div style="color:%s;font-size:13px;font-weight:800;letter-spacing:.16em">BLIX</div><h1 style="margin:12px 0 8px;font-size:28px">%s</h1><p style="margin:0;color:#756d78;line-height:1.6">%s</p></main></body></html>`, html.EscapeString(title), color, html.EscapeString(title), html.EscapeString(message))
}

func randomState() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func loadSession() (*tokenSession, error) {
	path, err := sessionPath()
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var session tokenSession
	if err := json.Unmarshal(contents, &session); err != nil || session.AccessToken == "" {
		_ = os.Remove(path)
		return nil, nil
	}
	return &session, nil
}

func saveSession(session tokenSession) error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	contents, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return os.WriteFile(path, contents, 0o600)
}

func removeSession() error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func sessionPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "blix", "session.json"), nil
}
