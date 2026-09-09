package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/chzyer/readline"
	"golang.org/x/term"
)

const agentID = "general_conversation_v1"

var (
	pink        = lipgloss.Color("#E105A3")
	muted       = lipgloss.Color("#9893A3")
	white       = lipgloss.Color("#F3F1F6")
	pinkStyle   = lipgloss.NewStyle().Bold(true).Foreground(pink)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(white)
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(pink).Padding(0, 2)
)

type client struct {
	baseURL string
	http    *http.Client
}

type run struct {
	ID              string `json:"run_id"`
	RootExecutionID string `json:"root_execution_id"`
}

type execution struct {
	ID string `json:"execution_id"`
}

type createdRun struct {
	Run       run       `json:"run"`
	Execution execution `json:"execution"`
}

type appendedMessage struct {
	EventPosition uint64 `json:"event_position"`
}

type executionEvent struct {
	Position uint64          `json:"position"`
	Type     string          `json:"type"`
	EntityID string          `json:"entity_id"`
	Payload  json.RawMessage `json:"payload"`
}

type eventsResponse struct {
	Events []executionEvent `json:"events"`
}

type toolCallPayload struct {
	ToolID string `json:"tool_id"`
}

type toolResultPayload struct {
	Status string `json:"status"`
}

type agentCallPayload struct {
	AgentID string `json:"agent_id"`
}

type agentResultPayload struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type assistantPayload struct {
	Content string `json:"content"`
}

type modelResponsePayload struct {
	Sequence     uint64 `json:"sequence"`
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
}

type failedPayload struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiError struct {
	Error string `json:"error"`
}

type eventsMsg struct {
	events []executionEvent
	err    error
}

type turnStatus struct {
	frames  []string
	frame   int
	enabled bool
}

type sessionInput struct {
	scanner   *bufio.Scanner
	editor    *readline.Instance
	closeOnce sync.Once
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runwayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("RUNWAY_URL")), "/")
	if runwayURL == "" {
		runwayURL = "http://localhost:10400"
	}
	api := &client{
		baseURL: runwayURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
	if err := runSession(ctx, api); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, errorPanel(err.Error()))
		os.Exit(1)
	}
}

func runSession(ctx context.Context, api *client) error {
	current, err := createRunWithRetry(ctx, api)
	if err != nil {
		return err
	}
	printBanner(current)

	input, err := newSessionInput()
	if err != nil {
		return fmt.Errorf("initialize terminal input: %w", err)
	}
	defer input.close()
	stopInput := context.AfterFunc(ctx, input.close)
	defer stopInput()

	for {
		line, err := input.read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, readline.ErrInterrupt) {
			return context.Canceled
		}
		if err != nil {
			return err
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		switch input {
		case "/quit":
			fmt.Println(mutedStyle.Render("Session ended."))
			return nil
		case "/help":
			printHelp()
			continue
		case "/reset":
			current, err = api.createRun(ctx)
			if err != nil {
				fmt.Println(errorPanel(err.Error()))
				continue
			}
			fmt.Println(mutedStyle.Render("Run reset."))
			printBanner(current)
			continue
		}
		if strings.HasPrefix(input, "/") {
			fmt.Println(errorPanel("Unknown command: " + input))
			continue
		}

		appended, err := api.appendMessage(ctx, current.Run.ID, input)
		if err != nil {
			fmt.Println(errorPanel(err.Error()))
			continue
		}
		fmt.Println()
		content, err := runTurn(ctx, api, current.Run.ID, appended.EventPosition)
		if err != nil {
			fmt.Println(errorPanel(err.Error()))
			continue
		}
		fmt.Println(responsePanel(content))
	}
}

func newSessionInput() (*sessionInput, error) {
	input := &sessionInput{scanner: bufio.NewScanner(os.Stdin)}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return input, nil
	}

	editor, err := readline.NewEx(&readline.Config{
		Prompt:          inputPrompt(),
		InterruptPrompt: "\n",
		EOFPrompt:       "\n",
		VimMode:         false,
	})
	if err != nil {
		return nil, err
	}
	input.editor = editor
	return input, nil
}

func (i *sessionInput) read() (string, error) {
	if i.editor != nil {
		return i.editor.Readline()
	}

	fmt.Print(inputPrompt())
	if !i.scanner.Scan() {
		if err := i.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return i.scanner.Text(), nil
}

func (i *sessionInput) close() {
	i.closeOnce.Do(func() {
		if i.editor != nil {
			_ = i.editor.Close()
		}
	})
}

func inputPrompt() string {
	return userStyle.Render("●  you") + " " + mutedStyle.Render("›") + " "
}

func createRunWithRetry(ctx context.Context, api *client) (createdRun, error) {
	var lastErr error
	for attempt := 0; attempt < 15; attempt++ {
		created, err := api.createRun(ctx)
		if err == nil {
			return created, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return createdRun{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return createdRun{}, fmt.Errorf("runway did not become ready: %w", lastErr)
}

func runTurn(ctx context.Context, api *client, runID string, after uint64) (string, error) {
	fmt.Println(pinkStyle.Render("●  blix"))

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	status := newTurnStatus()
	status.render()
	defer status.clear()

	ticker := time.NewTicker(spinner.Dot.FPS)
	defer ticker.Stop()
	events := watchEvents(turnCtx, api, runID, after)
	toolIDs := make(map[string]string)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			status.advance()
		case message, open := <-events:
			if !open {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", errors.New("runway event stream closed unexpectedly")
			}
			if message.err != nil {
				return "", message.err
			}
			for _, event := range message.events {
				content, activity, done, err := applyEvent(event, toolIDs)
				if activity != "" {
					status.printActivity(activity)
				}
				if done {
					return content, err
				}
			}
		}
	}
}

func newTurnStatus() *turnStatus {
	return &turnStatus{
		frames:  spinner.Dot.Frames,
		enabled: term.IsTerminal(int(os.Stdout.Fd())),
	}
}

func (s *turnStatus) advance() {
	if !s.enabled {
		return
	}
	s.frame = (s.frame + 1) % len(s.frames)
	s.render()
}

func (s *turnStatus) render() {
	if !s.enabled {
		return
	}
	frame := pinkStyle.Render(s.frames[s.frame])
	fmt.Print("\r\x1b[2K   " + frame + mutedStyle.Render("Blix is thinking…"))
}

func (s *turnStatus) clear() {
	if s.enabled {
		fmt.Print("\r\x1b[2K")
	}
}

func (s *turnStatus) printActivity(activity string) {
	s.clear()
	fmt.Println(activity)
	s.render()
}

func applyEvent(event executionEvent, toolIDs map[string]string) (string, string, bool, error) {
	switch event.Type {
	case "agent.call_v1":
		var payload agentCallPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			activity := mutedStyle.Render(fmt.Sprintf("   ↳  agent  %s  ·  queued", payload.AgentID))
			return "", activity, false, nil
		}
	case "agent.result_v1":
		var payload agentResultPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			activity := mutedStyle.Render(fmt.Sprintf("   ↳  agent  %s  ·  %s", payload.AgentID, payload.Status))
			return "", activity, false, nil
		}
	case "tool.call_v1":
		var payload toolCallPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			toolIDs[event.EntityID] = payload.ToolID
		}
	case "tool.result_v1":
		var payload toolResultPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			toolID := toolIDs[event.EntityID]
			if toolID == "" {
				toolID = "tool"
			}
			activity := mutedStyle.Render(fmt.Sprintf("   ↳  tool   %s  ·  %s", toolID, payload.Status))
			return "", activity, false, nil
		}
	case "model.response_v1":
		var payload modelResponsePayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			tokens := payload.InputTokens + payload.OutputTokens
			activity := mutedStyle.Render(fmt.Sprintf("   ↳  model  call %d  ·  %d tokens", payload.Sequence, tokens))
			return "", activity, false, nil
		}
	case "execution.lease_expired_v1":
		return "", mutedStyle.Render("   ↳  execution lease expired  ·  requeued"), false, nil
	case "message.assistant_v1":
		var payload assistantPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Content == "" {
			return "", "", true, errors.New("runway returned an invalid assistant message event")
		}
		return payload.Content, "", true, nil
	case "execution.failed_v1":
		var payload failedPayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Error.Message != "" {
			return "", "", true, errors.New(payload.Error.Message)
		}
		return "", "", true, errors.New("agent execution failed")
	}
	return "", "", false, nil
}

func watchEvents(
	ctx context.Context,
	api *client,
	runID string,
	after uint64,
) <-chan eventsMsg {
	messages := make(chan eventsMsg)
	go func() {
		defer close(messages)
		position := after
		for {
			events, err := api.listEvents(ctx, runID, position)
			if err != nil {
				select {
				case messages <- eventsMsg{err: err}:
				case <-ctx.Done():
				}
				return
			}
			for _, event := range events {
				position = event.Position
			}
			if len(events) > 0 {
				select {
				case messages <- eventsMsg{events: events}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return messages
}

func (c *client) createRun(ctx context.Context) (createdRun, error) {
	var created createdRun
	err := c.request(ctx, http.MethodPost, "/v1/runs", map[string]string{"agent_id": agentID}, &created)
	return created, err
}

func (c *client) appendMessage(ctx context.Context, runID string, content string) (appendedMessage, error) {
	var appended appendedMessage
	err := c.request(
		ctx,
		http.MethodPost,
		"/v1/runs/"+runID+"/messages",
		map[string]string{"content": content},
		&appended,
	)
	return appended, err
}

func (c *client) listEvents(ctx context.Context, runID string, after uint64) ([]executionEvent, error) {
	var response eventsResponse
	path := fmt.Sprintf("/v1/runs/%s/events?after=%d", runID, after)
	if err := c.request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Events, nil
}

func (c *client) request(ctx context.Context, method string, path string, payload any, destination any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("runway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		var apiErr apiError
		if json.NewDecoder(response.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return errors.New(apiErr.Error)
		}
		return fmt.Errorf("runway returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode runway response: %w", err)
	}
	return nil
}

func printBanner(current createdRun) {
	lines := []string{
		mutedStyle.Render("AGENT      ") + mutedStyle.Render(agentID),
		mutedStyle.Render("RUN        ") + mutedStyle.Render(current.Run.ID),
		mutedStyle.Render("EXECUTION  ") + mutedStyle.Render(current.Execution.ID),
		mutedStyle.Render("COMMANDS   ") + mutedStyle.Render("/help  /reset  /quit"),
	}
	width := terminalWidth() - 2
	if width < 60 {
		width = 60
	}
	panel := borderStyle.Width(width - 6).Render(strings.Join(lines, "\n"))
	title := pinkStyle.Render(" BLIX ")
	fmt.Println("\n" + title)
	fmt.Println(panel)
}

func printHelp() {
	content := strings.Join([]string{
		pinkStyle.Render("/help") + "   Show available commands",
		pinkStyle.Render("/reset") + "  Start a fresh run",
		pinkStyle.Render("/quit") + "   End the session",
	}, "\n")
	fmt.Println(borderStyle.Render(content))
}

func responsePanel(content string) string {
	width := terminalWidth() - 8
	if width < 50 {
		width = 50
	}
	content = renderMarkdown(content, width-4)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Padding(0, 2).
		Width(width).
		Render(content)
}

func renderMarkdown(content string, width int) string {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath("pink"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return content
	}
	rendered, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(rendered)
}

func errorPanel(message string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FF6B6B")).
		Foreground(lipgloss.Color("#FF6B6B")).
		Padding(0, 1).
		Render(message)
}

func terminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return 100
	}
	return width
}
