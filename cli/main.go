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
	"net/url"
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

const (
	blixUsersGroup         = "BlixUsers"
	nonmemberAccessMessage = "You do not have access to Blix! Please request to join the BlixUsers group."
	blixBanner             = `██████╗ ██╗     ██╗██╗  ██╗
██╔══██╗██║     ██║╚██╗██╔╝
██████╔╝██║     ██║ ╚███╔╝
██╔══██╗██║     ██║ ██╔██╗
██████╔╝███████╗██║██╔╝ ██╗
╚═════╝ ╚══════╝╚═╝╚═╝  ╚═╝`
)

var (
	pink       = lipgloss.Color("#F20AA6")
	muted      = lipgloss.Color("#918A95")
	white      = lipgloss.Color("#F3EFF4")
	green      = lipgloss.Color("#91CF7A")
	amber      = lipgloss.Color("#E4B866")
	red        = lipgloss.Color("#FF6B7A")
	surface    = lipgloss.Color("#211F22")
	pinkStyle  = lipgloss.NewStyle().Bold(true).Foreground(pink)
	mutedStyle = lipgloss.NewStyle().Foreground(muted)
	userStyle  = lipgloss.NewStyle().Bold(true).Foreground(white)
)

type client struct {
	baseURL string
	http    *http.Client
	session *tokenSession
	caller  *callerContext
	user    *currentUser
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
	Position    uint64          `json:"position"`
	ExecutionID string          `json:"execution_id"`
	Type        string          `json:"type"`
	EntityID    string          `json:"entity_id"`
	Payload     json.RawMessage `json:"payload"`
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

type httpStatusError struct {
	status  int
	message string
}

func (e *httpStatusError) Error() string {
	return e.message
}

type sessionRestart struct{}

func (sessionRestart) Error() string {
	return "restart CLI session"
}

type eventsMsg struct {
	events []executionEvent
	err    error
}

type turnStatus struct {
	frames          []string
	frame           int
	depth           int
	lastActivityKey string
	enabled         bool
}

type renderedActivity struct {
	key   string
	line  string
	depth int
}

type activityTree struct {
	depthByExecution map[string]int
	agentByExecution map[string]string
}

type sessionInput struct {
	scanner   *bufio.Scanner
	editor    *readline.Instance
	closeOnce sync.Once
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configuredURL := strings.TrimSpace(os.Getenv("BLIX_URL"))
	if configuredURL == "" {
		configuredURL = "http://localhost:10310"
	}
	blixURL, err := normalizeEndpoint(configuredURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorPanel(err.Error()))
		os.Exit(1)
	}
	api := &client{
		baseURL: blixURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
	stored, err := loadCredentials(blixURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorPanel("Could not load the saved credentials: "+err.Error()))
	} else {
		api.session = stored
	}
	for {
		err := runSession(ctx, api)
		var restart sessionRestart
		if errors.As(err, &restart) {
			if term.IsTerminal(int(os.Stdout.Fd())) {
				fmt.Print("\x1b[2J\x1b[H")
			}
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, errorPanel(err.Error()))
			os.Exit(1)
		}
		return
	}
}

func runSession(ctx context.Context, api *client) error {
	var current *createdRun
	var startupError error
	if api.session != nil {
		caller, err := api.getSession(ctx)
		if err == nil {
			api.caller = &caller
			user, userErr := api.getCurrentUser(ctx)
			if userErr != nil {
				startupError = fmt.Errorf("could not refresh your Sentinel profile: %w", userErr)
			} else {
				api.user = &user
				if api.canUseBlix() {
					created, createErr := createRunWithRetry(ctx, api)
					if createErr != nil {
						return createErr
					}
					current = &created
				} else {
					startupError = errors.New(nonmemberAccessMessage)
				}
			}
		} else {
			api.logout(ctx)
		}
	}
	printSessionPanel(current, api, startupError)

	terminalInput, err := newSessionInput()
	if err != nil {
		return fmt.Errorf("initialize terminal input: %w", err)
	}
	defer terminalInput.close()
	stopInput := context.AfterFunc(ctx, terminalInput.close)
	defer stopInput()

	for {
		line, err := terminalInput.read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, readline.ErrInterrupt) {
			return context.Canceled
		}
		if err != nil {
			return err
		}
		command := strings.TrimSpace(line)
		if command == "" {
			continue
		}
		switch command {
		case "/quit":
			fmt.Println(mutedStyle.Render("Session ended."))
			return nil
		case "/help":
			printHelp()
			continue
		case "/endpoint":
			fmt.Println(mutedStyle.Render("Connected to ") + userStyle.Render(api.baseURL))
			fmt.Println(mutedStyle.Render("Usage: ") + pinkStyle.Render("/endpoint <http-or-https-url>"))
			continue
		case "/login":
			_, err := api.login(ctx)
			if err != nil {
				fmt.Println(errorPanel(err.Error()))
				continue
			}
			if !api.canUseBlix() {
				current = nil
				printSessionPanel(current, api, errors.New(nonmemberAccessMessage))
				continue
			}
			created, err := createRunWithRetry(ctx, api)
			if err != nil {
				fmt.Println(errorPanel(err.Error()))
				continue
			}
			current = &created
			printSessionPanel(current, api, nil)
			continue
		case "/logout":
			api.logout(ctx)
			current = nil
			printSessionPanel(current, api, nil)
			continue
		case "/reset":
			if !api.canUseBlix() {
				fmt.Println(errorPanel(api.accessMessage()))
				continue
			}
			created, err := api.createRun(ctx)
			if err != nil {
				fmt.Println(errorPanel(err.Error()))
				continue
			}
			current = &created
			fmt.Println(mutedStyle.Render("Run reset."))
			printSessionPanel(current, api, nil)
			continue
		}
		if strings.HasPrefix(command, "/endpoint ") {
			endpoint, err := normalizeEndpoint(strings.TrimSpace(strings.TrimPrefix(command, "/endpoint")))
			if err != nil {
				fmt.Println(errorPanel(err.Error()))
				continue
			}
			if endpoint == api.baseURL {
				fmt.Println(mutedStyle.Render("Already connected to ") + userStyle.Render(endpoint))
				continue
			}
			stored, err := loadCredentials(endpoint)
			if err != nil {
				fmt.Println(errorPanel("Could not load credentials for that endpoint: " + err.Error()))
				continue
			}
			api.baseURL = endpoint
			api.session = stored
			api.caller = nil
			api.user = nil
			return sessionRestart{}
		}
		if strings.HasPrefix(command, "/") {
			fmt.Println(errorPanel("Unknown command: " + command))
			continue
		}
		if current == nil {
			fmt.Println(errorPanel(api.accessMessage()))
			continue
		}

		appended, err := api.appendMessage(ctx, current.Run.ID, command)
		if err != nil {
			fmt.Println(errorPanel(err.Error()))
			continue
		}
		fmt.Println()
		content, err := runTurn(ctx, api, current.Run.ID, current.Execution.ID, appended.EventPosition)
		if err != nil {
			fmt.Println(errorPanel(err.Error()))
			continue
		}
		fmt.Println(responsePanel(content))
	}
}

func normalizeEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid Blix endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Blix endpoint must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("Blix endpoint must include a host")
	}
	if parsed.User != nil {
		return "", errors.New("Blix endpoint cannot include credentials")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("Blix endpoint cannot include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Blix endpoint cannot include a query or fragment")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
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
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(pink).
		Background(surface).
		PaddingLeft(1).
		PaddingRight(1).
		Render("❯") + " "
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
	return createdRun{}, fmt.Errorf("Blix did not become ready: %w", lastErr)
}

func runTurn(
	ctx context.Context,
	api *client,
	runID string,
	rootExecutionID string,
	after uint64,
) (string, error) {
	fmt.Println(agentHeader("working"))

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	status := newTurnStatus()
	status.render()
	defer status.clear()

	ticker := time.NewTicker(spinner.Dot.FPS)
	defer ticker.Stop()
	events := watchEvents(turnCtx, api, runID, after)
	toolIDs := make(map[string]string)
	tree := newActivityTree(rootExecutionID)

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
				return "", errors.New("Blix event stream closed unexpectedly")
			}
			if message.err != nil {
				return "", message.err
			}
			for _, event := range message.events {
				content, activity, done, err := applyEvent(event, toolIDs, tree)
				if activity != nil {
					status.printActivity(*activity)
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
	prefix := "  " + strings.Repeat("│  ", s.depth) + "│"
	fmt.Print("\r\x1b[2K" + mutedStyle.Render(prefix) + " " + frame + " " + mutedStyle.Render("thinking"))
}

func (s *turnStatus) clear() {
	if s.enabled {
		fmt.Print("\r\x1b[2K")
	}
}

func (s *turnStatus) printActivity(activity renderedActivity) {
	s.clear()
	if s.enabled && activity.key != "" && activity.key == s.lastActivityKey {
		fmt.Print("\x1b[1A\r\x1b[2K")
	}
	fmt.Println(activity.line)
	s.depth = activity.depth
	s.lastActivityKey = activity.key
	s.render()
}

func newActivityTree(rootExecutionID string) *activityTree {
	return &activityTree{
		depthByExecution: map[string]int{rootExecutionID: 0},
		agentByExecution: map[string]string{rootExecutionID: agentID},
	}
}

func (t *activityTree) executionDepth(executionID string) int {
	depth, ok := t.depthByExecution[executionID]
	if !ok {
		return 0
	}
	return depth
}

func (t *activityTree) agentCall(parentExecutionID string, childExecutionID string, childAgentID string) int {
	parentDepth := t.executionDepth(parentExecutionID)
	t.depthByExecution[childExecutionID] = parentDepth + 1
	t.agentByExecution[childExecutionID] = childAgentID
	return parentDepth
}

func applyEvent(
	event executionEvent,
	toolIDs map[string]string,
	tree *activityTree,
) (string, *renderedActivity, bool, error) {
	switch event.Type {
	case "agent.call_v1":
		var payload agentCallPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			depth := tree.agentCall(event.ExecutionID, event.EntityID, payload.AgentID)
			activity := newRenderedActivity("agent:"+event.EntityID, depth, "agent", payload.AgentID, "queued")
			return "", &activity, false, nil
		}
	case "execution.claimed_v1":
		agent, ok := tree.agentByExecution[event.ExecutionID]
		if ok && event.ExecutionID != "" {
			depth := tree.executionDepth(event.ExecutionID) - 1
			if depth < 0 {
				return "", nil, false, nil
			}
			activity := newRenderedActivity("agent:"+event.ExecutionID, depth, "agent", agent, "running")
			return "", &activity, false, nil
		}
	case "agent.result_v1":
		var payload agentResultPayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			agent := payload.AgentID
			if agent == "" {
				agent = tree.agentByExecution[event.EntityID]
			}
			activity := newRenderedActivity(
				"agent:"+event.EntityID,
				tree.executionDepth(event.ExecutionID),
				"agent",
				agent,
				payload.Status,
			)
			return "", &activity, false, nil
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
			activity := newRenderedActivity(
				"tool:"+event.EntityID,
				tree.executionDepth(event.ExecutionID),
				"tool",
				toolID,
				payload.Status,
			)
			return "", &activity, false, nil
		}
	case "model.response_v1":
		var payload modelResponsePayload
		if json.Unmarshal(event.Payload, &payload) == nil {
			activity := newRenderedActivity(
				fmt.Sprintf("model:%s:%d", event.ExecutionID, payload.Sequence),
				tree.executionDepth(event.ExecutionID),
				"model",
				fmt.Sprintf("call %d", payload.Sequence),
				"done",
			)
			return "", &activity, false, nil
		}
	case "execution.lease_expired_v1":
		activity := newRenderedActivity(
			"execution:"+event.ExecutionID,
			tree.executionDepth(event.ExecutionID),
			"runtime",
			"execution lease expired",
			"requeued",
		)
		return "", &activity, false, nil
	case "message.assistant_v1":
		var payload assistantPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Content == "" {
			return "", nil, true, errors.New("Blix returned an invalid assistant message event")
		}
		return payload.Content, nil, true, nil
	case "execution.failed_v1":
		var payload failedPayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Error.Message != "" {
			return "", nil, true, errors.New(payload.Error.Message)
		}
		return "", nil, true, errors.New("agent execution failed")
	}
	return "", nil, false, nil
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
	err := c.request(ctx, http.MethodPost, "/api/v1/runs", map[string]string{"agent_id": agentID}, &created)
	return created, err
}

func (c *client) appendMessage(ctx context.Context, runID string, content string) (appendedMessage, error) {
	var appended appendedMessage
	err := c.request(
		ctx,
		http.MethodPost,
		"/api/v1/runs/"+runID+"/messages",
		map[string]string{"content": content},
		&appended,
	)
	return appended, err
}

func (c *client) listEvents(ctx context.Context, runID string, after uint64) ([]executionEvent, error) {
	var response eventsResponse
	path := fmt.Sprintf("/api/v1/runs/%s/events?after=%d", runID, after)
	if err := c.request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Events, nil
}

func (c *client) request(ctx context.Context, method string, path string, payload any, destination any) error {
	err := c.doRequest(ctx, method, path, payload, destination, true)
	var statusError *httpStatusError
	if !errors.As(err, &statusError) || statusError.status != http.StatusUnauthorized || c.session == nil || c.session.RefreshToken == "" {
		return err
	}
	if refreshErr := c.refreshSession(ctx); refreshErr != nil {
		c.session = nil
		c.caller = nil
		c.user = nil
		_ = removeCredentials()
		return err
	}
	return c.doRequest(ctx, method, path, payload, destination, true)
}

func (c *client) canUseBlix() bool {
	if c.caller == nil || c.user == nil {
		return false
	}
	if c.caller.EntityType != "USER" || c.caller.UserID != c.user.ID || c.caller.EntityID != c.user.EntityID {
		return false
	}
	for _, group := range c.user.Groups {
		if group == blixUsersGroup {
			return true
		}
	}
	return false
}

func (c *client) accessMessage() string {
	if c.caller == nil {
		return "Sign in with /login before starting a run."
	}
	if c.user == nil {
		return "Your Sentinel profile could not be verified. Sign in again with /login."
	}
	return nonmemberAccessMessage
}

func (c *client) requestWithoutAuth(ctx context.Context, method string, path string, payload any, destination any) error {
	return c.doRequest(ctx, method, path, payload, destination, false)
}

func (c *client) doRequest(
	ctx context.Context,
	method string,
	path string,
	payload any,
	destination any,
	includeAuth bool,
) error {
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
	if includeAuth && c.session != nil && c.session.AccessToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.session.AccessToken)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Blix request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		var apiErr apiError
		if json.NewDecoder(response.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return &httpStatusError{status: response.StatusCode, message: apiErr.Error}
		}
		return &httpStatusError{status: response.StatusCode, message: fmt.Sprintf("Blix returned HTTP %d", response.StatusCode)}
	}
	if destination == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode Blix response: %w", err)
	}
	return nil
}

func printSessionPanel(current *createdRun, api *client, startupError error) {
	width := panelWidth(132)
	if width < 60 {
		width = 60
	}
	contentWidth := width - 8
	if contentWidth < 20 {
		contentWidth = 20
	}

	stateLabel := "ready"
	stateColor := green
	if api.caller == nil {
		stateLabel = "signed out"
		stateColor = amber
	} else if !api.canUseBlix() {
		stateLabel = "no access"
		stateColor = red
	} else if startupError != nil {
		stateLabel = "degraded"
		stateColor = amber
	}
	welcome := strings.Join(welcomeLines(api, startupError), "\n")
	metadata := sessionMetadata(current, api.caller, api.baseURL)
	status := lipgloss.NewStyle().Bold(true).Foreground(stateColor).Render("● " + stateLabel)
	brand := pinkStyle.Render(blixBanner)
	body := brand + "\n\n" + welcome + "\n\n" + status + "\n\n" + metadata
	if contentWidth >= 96 {
		leftWidth := 36
		rightWidth := contentWidth - leftWidth - 3
		left := brand + "\n\n" + welcome
		right := mutedStyle.Render("session") + "  " + status + "\n\n" + metadata
		bodyHeight := max(lipgloss.Height(left), lipgloss.Height(right))
		body = lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Width(leftWidth).Render(left),
			verticalRule(bodyHeight),
			"  ",
			lipgloss.NewStyle().Width(rightWidth).Render(right),
		)
	}

	commands := mutedStyle.Render("/login  /logout  /endpoint  /help  /reset  /quit")
	content := strings.Join([]string{
		body,
		mutedStyle.Render(strings.Repeat("─", contentWidth)),
		commands,
	}, "\n")
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pink).
		Padding(1, 3).
		Width(width)
	fmt.Println(style.Render(content))
}

func sessionMetadata(current *createdRun, caller *callerContext, endpoint string) string {
	runID := "—"
	executionID := "—"
	callerID := "not authenticated"
	if current != nil {
		runID = current.Run.ID
		executionID = current.Execution.ID
	}
	if caller != nil {
		callerID = caller.EntityID
	}
	labelStyle := lipgloss.NewStyle().Foreground(muted).Width(11)
	valueStyle := lipgloss.NewStyle().Bold(true).Foreground(white)
	rows := []struct {
		label string
		value string
	}{
		{label: "endpoint", value: endpoint},
		{label: "agent", value: agentID},
		{label: "caller", value: callerID},
		{label: "run", value: runID},
		{label: "execution", value: executionID},
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, lipgloss.JoinHorizontal(
			lipgloss.Top,
			labelStyle.Render(row.label),
			valueStyle.Render(row.value),
		))
	}
	return strings.Join(lines, "\n")
}

func welcomeLines(api *client, startupError error) []string {
	if api.caller == nil {
		return []string{
			pinkStyle.Render("Welcome to Blix"),
			mutedStyle.Render("Sign in with Sentinel to start a conversation."),
			mutedStyle.Render("Run ") + pinkStyle.Render("/login") + mutedStyle.Render(" to continue."),
		}
	}
	if api.user == nil {
		message := "Your Sentinel profile could not be refreshed."
		if startupError != nil {
			message = startupError.Error()
		}
		return []string{
			pinkStyle.Render("Signed in"),
			mutedStyle.Render(api.caller.EntityID),
			lipgloss.NewStyle().Foreground(red).Render(message),
		}
	}
	name := strings.TrimSpace(api.user.FirstName + " " + api.user.LastName)
	if name == "" {
		name = api.user.Username
	}
	if name == "" {
		name = api.user.Email
	}
	identity := strings.TrimSpace(api.user.Email)
	if api.user.Username != "" {
		if identity != "" {
			identity += "  ·  "
		}
		identity += "@" + api.user.Username
	}
	content := []string{pinkStyle.Render("Welcome back, " + name), mutedStyle.Render(identity)}
	if !api.canUseBlix() {
		content = append(content, lipgloss.NewStyle().Foreground(red).Render(nonmemberAccessMessage))
	}
	if startupError != nil && api.canUseBlix() {
		content = append(content, lipgloss.NewStyle().Foreground(red).Render(startupError.Error()))
	}
	return content
}

func printHelp() {
	commands := []struct {
		command string
		detail  string
	}{
		{command: "/login", detail: "Sign in with Sentinel"},
		{command: "/logout", detail: "Sign out and clear the saved session"},
		{command: "/endpoint", detail: "Show or change the active Blix endpoint"},
		{command: "/help", detail: "Show available commands"},
		{command: "/reset", detail: "Start a fresh run"},
		{command: "/quit", detail: "End the session"},
	}
	lines := make([]string, 0, len(commands)+1)
	lines = append(lines, pinkStyle.Render("COMMANDS"))
	for _, command := range commands {
		lines = append(lines, lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Bold(true).Foreground(white).Width(11).Render(command.command),
			mutedStyle.Render(command.detail),
		))
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Padding(0, 2).
		Width(panelWidth(76))
	fmt.Println(style.Render(strings.Join(lines, "\n")))
}

func responsePanel(content string) string {
	width := panelWidth(112)
	content = renderMarkdown(content, width-6)
	return lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderLeftForeground(pink).
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
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderLeftForeground(red).
		Foreground(red).
		Padding(0, 1).
		Render(message)
}

func agentHeader(status string) string {
	left := lipgloss.NewStyle().Bold(true).Foreground(pink).Render("◆  blix")
	return left + mutedStyle.Render("  ·  "+status)
}

func newRenderedActivity(key string, depth int, kind string, label string, status string) renderedActivity {
	return renderedActivity{
		key:   key,
		line:  activityLine(depth, kind, label, status),
		depth: depth,
	}
}

func activityLine(depth int, kind string, label string, status string) string {
	width := panelWidth(78)
	status = strings.ToLower(strings.TrimSpace(status))
	statusStyle := mutedStyle
	switch status {
	case "succeeded", "completed", "complete", "done", "ok":
		statusStyle = lipgloss.NewStyle().Foreground(green)
	case "queued", "waiting", "requeued":
		statusStyle = lipgloss.NewStyle().Foreground(amber)
	case "failed", "error":
		statusStyle = lipgloss.NewStyle().Foreground(red)
	case "running", "working":
		statusStyle = lipgloss.NewStyle().Foreground(pink)
	}

	right := ""
	if status != "" {
		right = statusStyle.Render("[" + status + "]")
	}
	prefix := "  " + strings.Repeat("│  ", depth) + "├─ "
	kindLabel := lipgloss.NewStyle().Bold(true).Foreground(pink).Width(7).Render(kind)
	label = truncateText(label, width-lipgloss.Width(prefix)-lipgloss.Width(kindLabel)-lipgloss.Width(right)-1)
	left := lipgloss.JoinHorizontal(
		lipgloss.Top,
		mutedStyle.Render(prefix),
		kindLabel,
		mutedStyle.Render(label),
	)
	return alignedRow(left, right, width)
}

func verticalRule(height int) string {
	if height < 1 {
		return ""
	}
	lines := make([]string, height)
	for index := range lines {
		lines[index] = mutedStyle.Render("│")
	}
	return strings.Join(lines, "\n")
}

func alignedRow(left string, right string, width int) string {
	spacing := width - lipgloss.Width(left) - lipgloss.Width(right)
	if spacing < 1 {
		spacing = 1
	}
	return left + strings.Repeat(" ", spacing) + right
}

func truncateText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func panelWidth(maximum int) int {
	width := terminalWidth() - 8
	if width < 32 {
		width = 32
	}
	if width > maximum {
		width = maximum
	}
	return width
}

func terminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return 100
	}
	return width
}
