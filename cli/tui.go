package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

type transcriptBlockKind int

const (
	transcriptUser transcriptBlockKind = iota
	transcriptTurn
	transcriptNotice
	transcriptError
)

type transcriptBlock struct {
	kind    transcriptBlockKind
	content string
	turn    *turnView
}

type turnView struct {
	activities          []renderedActivity
	activityIndex       map[string]int
	toolIDs             map[string]string
	completedExecutions map[string]bool
	rootExecutionID     string
	tree                *activityTree
	startedAt           time.Time
	finishedAt          time.Time
	response            string
	err                 error
}

type tuiModel struct {
	ctx                  context.Context
	api                  *client
	current              *createdRun
	caller               *callerContext
	user                 *currentUser
	startupError         error
	viewport             viewport.Model
	input                textinput.Model
	spinner              spinner.Model
	blocks               []transcriptBlock
	history              []string
	historyIndex         int
	historyDraft         string
	activeTurn           *turnView
	activeTurnID         uint64
	nextTurnID           uint64
	backgroundCursor     uint64
	backgroundExecutions map[string]bool
	width                int
	height               int
	initialized          bool
	busy                 bool
	operation            string
}

type sessionLoadedMsg struct {
	api          *client
	current      *createdRun
	caller       *callerContext
	user         *currentUser
	startupError error
	err          error
	clearBlocks  bool
}

type runCreatedMsg struct {
	current createdRun
	err     error
}

type messageAppendedMsg struct {
	turnID  uint64
	current createdRun
	result  appendedMessage
	err     error
}

type queuedMessageMsg struct {
	content string
	err     error
}

type interruptedMsg struct {
	err error
}

type executionEventsMsg struct {
	turnID uint64
	after  uint64
	events []executionEvent
	err    error
}

type backgroundEventsMsg struct {
	runID  string
	after  uint64
	events []executionEvent
	err    error
}

func runTUI(ctx context.Context, api *client) error {
	program := tea.NewProgram(
		newTUIModel(ctx, api),
		tea.WithContext(ctx),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithoutSignalHandler(),
	)
	_, err := program.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func newTUIModel(ctx context.Context, api *client) *tuiModel {
	input := textinput.New()
	input.Prompt = "❯  "
	input.SetVirtualCursor(true)
	inputStyles := textinput.DefaultDarkStyles()
	inputStyles.Focused.Prompt = lipgloss.NewStyle().Bold(true).Foreground(pink).Background(userSurface)
	inputStyles.Focused.Text = lipgloss.NewStyle().Foreground(white).Background(userSurface)
	inputStyles.Focused.Placeholder = lipgloss.NewStyle().Foreground(muted).Background(userSurface)
	inputStyles.Focused.Suggestion = lipgloss.NewStyle().Foreground(muted).Background(userSurface)
	inputStyles.Cursor.Color = pink
	inputStyles.Cursor.Blink = false
	input.SetStyles(inputStyles)
	input.Focus()

	view := viewport.New()
	view.FillHeight = true
	view.SoftWrap = false
	view.MouseWheelEnabled = false

	loader := spinner.New(spinner.WithSpinner(spinner.Dot))
	loader.Style = pinkStyle

	return &tuiModel{
		ctx:                  ctx,
		api:                  api,
		viewport:             view,
		input:                input,
		spinner:              loader,
		historyIndex:         0,
		backgroundExecutions: make(map[string]bool),
		busy:                 true,
		operation:            "Connecting to Blix",
	}
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, loadSessionCmd(m.ctx, m.api, false))
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		return m, nil
	case tea.KeyPressMsg:
		return m, m.handleKey(message)
	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var command tea.Cmd
		m.spinner, command = m.spinner.Update(message)
		m.syncTranscript(false)
		return m, command
	case sessionLoadedMsg:
		return m, m.handleSessionLoaded(message)
	case runCreatedMsg:
		return m, m.handleRunCreated(message)
	case messageAppendedMsg:
		return m, m.handleMessageAppended(message)
	case queuedMessageMsg:
		if message.err != nil {
			m.appendBlock(transcriptError, message.err.Error(), nil)
		} else {
			m.appendBlock(transcriptNotice, "Queued message\n"+message.content, nil)
		}
		return m, nil
	case interruptedMsg:
		if message.err != nil {
			m.appendBlock(transcriptError, message.err.Error(), nil)
		} else {
			m.appendBlock(transcriptNotice, "Stopping the active turn; queued messages will continue.", nil)
		}
		return m, nil
	case executionEventsMsg:
		return m, m.handleExecutionEvents(message)
	case backgroundEventsMsg:
		return m, m.handleBackgroundEvents(message)
	}

	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return m, command
}

func (m *tuiModel) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("")
	}

	input := m.renderInput()
	footer := m.renderFooter()
	content := lipgloss.JoinVertical(lipgloss.Left, m.viewport.View(), "", input, footer)
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Blix"
	return view
}

func (m *tuiModel) handleKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.String()
	if key == "ctrl+c" {
		return tea.Quit
	}

	switch key {
	case "pgup", "pgdown":
		var command tea.Cmd
		m.viewport, command = m.viewport.Update(message)
		return command
	case "up":
		m.viewport.ScrollUp(3)
		return nil
	case "down":
		m.viewport.ScrollDown(3)
		return nil
	case "ctrl+up":
		m.viewport.ScrollUp(1)
		return nil
	case "ctrl+down":
		m.viewport.ScrollDown(1)
		return nil
	}
	switch key {
	case "ctrl+p":
		m.historyPrevious()
		return nil
	case "ctrl+n":
		m.historyNext()
		return nil
	case "enter":
		return m.submitInput()
	}

	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return command
}

func (m *tuiModel) submitInput() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		return nil
	}
	m.rememberInput(value)
	m.input.Reset()

	if strings.HasPrefix(value, "/") {
		return m.handleCommand(value)
	}
	if m.busy {
		m.appendBlock(transcriptNotice, "Blix is working. Use /queue <message> to queue work or /stop to interrupt.", nil)
		return nil
	}
	if m.current == nil {
		m.appendBlock(transcriptError, accessMessage(m.caller, m.user), nil)
		return nil
	}

	m.appendBlock(transcriptUser, value, nil)
	m.nextTurnID++
	m.activeTurnID = m.nextTurnID
	m.activeTurn = &turnView{
		activityIndex:       make(map[string]int),
		toolIDs:             make(map[string]string),
		completedExecutions: make(map[string]bool),
		rootExecutionID:     m.current.Execution.ID,
		tree:                newActivityTree(m.current.Execution.ID),
		startedAt:           time.Now(),
	}
	m.blocks = append(m.blocks, transcriptBlock{kind: transcriptTurn, turn: m.activeTurn})
	m.setBusy("Blix is working")
	m.syncTranscript(true)
	return tea.Batch(
		appendMessageCmd(m.ctx, m.api, m.activeTurnID, *m.current, value),
		m.spinner.Tick,
	)
}

func (m *tuiModel) handleCommand(command string) tea.Cmd {
	switch command {
	case "/quit":
		return tea.Quit
	case "/help":
		m.appendBlock(transcriptNotice, renderHelpPanel(m.contentWidth()), nil)
		return nil
	case "/endpoint":
		m.appendBlock(transcriptNotice, "Connected to "+m.api.baseURL+"\nUsage: /endpoint <http-or-https-url>", nil)
		return nil
	case "/login":
		m.setBusy("Waiting for Sentinel login")
		return tea.Batch(loginCmd(m.ctx, m.api), m.spinner.Tick)
	case "/logout":
		m.setBusy("Signing out")
		return tea.Batch(logoutCmd(m.ctx, m.api), m.spinner.Tick)
	case "/reset":
		if !canUseBlixIdentity(m.caller, m.user) {
			m.appendBlock(transcriptError, accessMessage(m.caller, m.user), nil)
			return nil
		}
		m.setBusy("Starting a new run")
		return tea.Batch(createRunCmd(m.ctx, m.api), m.spinner.Tick)
	case "/stop":
		if m.current == nil {
			return nil
		}
		return interruptRunCmd(m.ctx, m.api, m.current.Run.ID)
	}
	if strings.HasPrefix(command, "/queue ") {
		content := strings.TrimSpace(strings.TrimPrefix(command, "/queue"))
		if content == "" || m.current == nil {
			return nil
		}
		return queueMessageCmd(m.ctx, m.api, m.current.Run.ID, content)
	}

	if strings.HasPrefix(command, "/endpoint ") {
		endpoint, err := normalizeEndpoint(strings.TrimSpace(strings.TrimPrefix(command, "/endpoint")))
		if err != nil {
			m.appendBlock(transcriptError, err.Error(), nil)
			return nil
		}
		if endpoint == m.api.baseURL {
			m.appendBlock(transcriptNotice, "Already connected to "+endpoint, nil)
			return nil
		}
		m.setBusy("Connecting to " + endpoint)
		return tea.Batch(switchEndpointCmd(m.ctx, m.api.http, endpoint), m.spinner.Tick)
	}

	m.appendBlock(transcriptError, "Unknown command: "+command, nil)
	return nil
}

func (m *tuiModel) handleSessionLoaded(message sessionLoadedMsg) tea.Cmd {
	wasInitialized := m.initialized
	m.busy = false
	m.operation = ""
	m.initialized = true
	if message.err != nil {
		if !wasInitialized {
			m.startupError = message.err
		}
		m.appendBlock(transcriptError, message.err.Error(), nil)
		m.input.Focus()
		m.syncTranscript(true)
		return nil
	}
	if message.api != nil {
		m.api = message.api
	}
	m.current = message.current
	if message.clearBlocks {
		m.backgroundCursor = 0
		m.backgroundExecutions = make(map[string]bool)
	}
	m.caller = message.caller
	m.user = message.user
	m.startupError = message.startupError
	if message.clearBlocks {
		m.blocks = nil
	}
	m.input.Focus()
	m.resizeInput()
	m.syncTranscript(true)
	if m.current != nil {
		return pollBackgroundEventsCmd(m.ctx, m.api, m.current.Run.ID, 0)
	}
	return nil
}

func (m *tuiModel) handleRunCreated(message runCreatedMsg) tea.Cmd {
	m.busy = false
	m.operation = ""
	if message.err != nil {
		m.appendBlock(transcriptError, message.err.Error(), nil)
		m.input.Focus()
		return nil
	}
	m.current = &message.current
	m.blocks = nil
	m.backgroundCursor = 0
	m.backgroundExecutions = make(map[string]bool)
	m.input.Focus()
	m.syncTranscript(true)
	return pollBackgroundEventsCmd(m.ctx, m.api, message.current.Run.ID, 0)
}

func (m *tuiModel) handleMessageAppended(message messageAppendedMsg) tea.Cmd {
	if message.turnID != m.activeTurnID || m.activeTurn == nil {
		return nil
	}
	if message.err != nil {
		return m.finishTurn("", message.err, 0)
	}
	m.current = &message.current
	return pollEventsCmd(m.ctx, m.api, message.turnID, message.current.Run.ID, message.result.EventPosition)
}

func (m *tuiModel) handleExecutionEvents(message executionEventsMsg) tea.Cmd {
	if message.turnID != m.activeTurnID || m.activeTurn == nil {
		return nil
	}
	if message.err != nil {
		return m.finishTurn("", message.err, message.after)
	}

	position := message.after
	for _, event := range message.events {
		position = event.Position
		m.backgroundCursor = max(m.backgroundCursor, event.Position)
		if event.Type == "agent.call_v1" {
			var payload agentCallPayload
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Background {
				m.backgroundExecutions[event.EntityID] = true
			}
		}
		content, activity, done, err := applyEvent(event, m.activeTurn.toolIDs, m.activeTurn.tree)
		if activity != nil {
			m.activeTurn.upsertActivity(*activity)
		}
		if event.Type == "agent.result_v1" && event.EntityID != "" {
			m.activeTurn.completedExecutions[event.EntityID] = true
		}
		if done {
			return m.finishTurn(content, err, position)
		}
	}
	m.syncTranscript(true)
	return pollEventsCmd(m.ctx, m.api, message.turnID, m.current.Run.ID, position)
}

func (m *tuiModel) finishTurn(content string, err error, after uint64) tea.Cmd {
	m.activeTurn.response = content
	m.activeTurn.err = err
	m.activeTurn.finishedAt = time.Now()
	m.activeTurn.completedExecutions[m.activeTurn.rootExecutionID] = true
	m.activeTurn = nil
	m.busy = false
	m.operation = ""
	m.input.Focus()
	m.syncTranscript(true)
	return nil
}

func (m *tuiModel) handleBackgroundEvents(message backgroundEventsMsg) tea.Cmd {
	if m.current == nil || m.current.Run.ID != message.runID {
		return nil
	}
	if m.activeTurn != nil {
		return pollBackgroundEventsCmd(m.ctx, m.api, message.runID, max(message.after, m.backgroundCursor))
	}
	if message.err != nil {
		return pollBackgroundEventsCmd(m.ctx, m.api, message.runID, message.after)
	}
	position := max(message.after, m.backgroundCursor)
	for _, event := range message.events {
		if event.Position <= m.backgroundCursor {
			continue
		}
		position = event.Position
		m.backgroundCursor = event.Position
		switch event.Type {
		case "agent.call_v1":
			var payload agentCallPayload
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Background {
				m.backgroundExecutions[event.EntityID] = true
				m.appendBlock(transcriptNotice, fmt.Sprintf("Background agent started  %s\n%s", payload.AgentID, event.EntityID), nil)
			}
		case "agent.result_v1":
			if !m.backgroundExecutions[event.EntityID] {
				continue
			}
			var payload agentResultPayload
			if json.Unmarshal(event.Payload, &payload) == nil {
				kind := transcriptNotice
				if payload.Status == "failed" {
					kind = transcriptError
				}
				m.appendBlock(kind, fmt.Sprintf("Background agent %s  %s\n%s", payload.Status, payload.AgentID, event.EntityID), nil)
			}
		case "message.assistant_v1":
			var payload assistantPayload
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Content != "" {
				turn := &turnView{startedAt: time.Now(), finishedAt: time.Now(), response: payload.Content}
				m.blocks = append(m.blocks, transcriptBlock{kind: transcriptTurn, turn: turn})
				m.syncTranscript(true)
			}
		}
	}
	return pollBackgroundEventsCmd(m.ctx, m.api, message.runID, position)
}

func (m *tuiModel) setBusy(operation string) {
	m.busy = true
	m.operation = operation
	m.syncTranscript(true)
}

func (m *tuiModel) appendBlock(kind transcriptBlockKind, content string, turn *turnView) {
	m.blocks = append(m.blocks, transcriptBlock{kind: kind, content: content, turn: turn})
	m.syncTranscript(true)
}

func (m *tuiModel) syncTranscript(forceBottom bool) {
	if m.width == 0 || m.height == 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderTranscript())
	if forceBottom || wasAtBottom {
		m.viewport.GotoBottom()
	}
}

func (m *tuiModel) renderTranscript() string {
	parts := []string{renderSessionPanel(m.current, m.caller, m.user, m.api.baseURL, m.startupError, m.width)}
	for _, block := range m.blocks {
		switch block.kind {
		case transcriptUser:
			parts = append(parts, renderUserMessage(block.content, m.contentWidth()))
		case transcriptTurn:
			parts = append(parts, m.renderTurn(block.turn))
		case transcriptNotice:
			parts = append(parts, renderNoticePanel(block.content, m.contentWidth(), muted))
		case transcriptError:
			parts = append(parts, renderNoticePanel(block.content, m.contentWidth(), red))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (m *tuiModel) renderTurn(turn *turnView) string {
	status := "working"
	if !turn.finishedAt.IsZero() {
		verb := "finished"
		if turn.err != nil {
			verb = "failed"
		}
		status = fmt.Sprintf("%s in %s", verb, formatDuration(turn.finishedAt.Sub(turn.startedAt)))
	}
	parts := []string{renderAgentHeader(status, m.contentWidth())}
	activities := turn.visibleActivities()
	if len(activities) > 0 {
		rows := make([]string, 0, len(activities))
		for _, activity := range activities {
			rows = append(rows, renderActivityLine(activity, m.contentWidth()))
		}
		parts = append(parts, strings.Join(rows, "\n"))
	}
	if turn.finishedAt.IsZero() {
		depth := 0
		if len(turn.activities) > 0 {
			depth = turn.activities[len(turn.activities)-1].depth
		}
		thinking := strings.Repeat(" ", chatInset+depth*4) + m.spinner.View() + " " + mutedStyle.Render("thinking")
		parts = append(parts, thinking)
	}
	if turn.err != nil {
		parts = append(parts, renderNoticePanel(turn.err.Error(), m.contentWidth(), red))
	} else if turn.response != "" {
		parts = append(parts, renderResponse(turn.response, m.contentWidth()))
	}
	return strings.Join(parts, "\n\n")
}

func (m *tuiModel) renderInput() string {
	return renderMessageSurface(m.input.View(), m.contentWidth(), 1)
}

func (m *tuiModel) renderFooter() string {
	left := mutedStyle.Render("  ↑/↓ scroll  ·  pgup/pgdn page  ·  ctrl+p/n history")
	rightLabel := "ctrl+c quit  "
	if m.busy {
		operation := m.operation
		if operation == "" {
			operation = "Blix is working"
		}
		rightLabel = operation + " · enter waits  "
	}
	rightWidth := m.width - lipgloss.Width(left) - 1
	right := ""
	if rightWidth > 0 {
		right = mutedStyle.Render(truncateText(rightLabel, rightWidth))
	}
	return alignedRow(left, right, m.width)
}

func (m *tuiModel) resize(width int, height int) {
	m.width = max(40, width)
	m.height = max(12, height)
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(1, m.height-5))
	m.resizeInput()
	m.syncTranscript(true)
}

func (m *tuiModel) resizeInput() {
	width := m.contentWidth() - 6 - lipgloss.Width(m.input.Prompt)
	m.input.SetWidth(max(1, width))
}

func (m *tuiModel) contentWidth() int {
	return max(32, m.width-chatInset*2)
}

func (m *tuiModel) rememberInput(value string) {
	if len(m.history) == 0 || m.history[len(m.history)-1] != value {
		m.history = append(m.history, value)
	}
	m.historyIndex = len(m.history)
	m.historyDraft = ""
}

func (m *tuiModel) historyPrevious() {
	if len(m.history) == 0 || m.historyIndex == 0 {
		return
	}
	if m.historyIndex == len(m.history) {
		m.historyDraft = m.input.Value()
	}
	m.historyIndex--
	m.input.SetValue(m.history[m.historyIndex])
	m.input.CursorEnd()
}

func (m *tuiModel) historyNext() {
	if m.historyIndex >= len(m.history) {
		return
	}
	m.historyIndex++
	if m.historyIndex == len(m.history) {
		m.input.SetValue(m.historyDraft)
	} else {
		m.input.SetValue(m.history[m.historyIndex])
	}
	m.input.CursorEnd()
}

func (turn *turnView) upsertActivity(activity renderedActivity) {
	if index, ok := turn.activityIndex[activity.key]; ok {
		turn.activities[index] = activity
		return
	}
	turn.activityIndex[activity.key] = len(turn.activities)
	turn.activities = append(turn.activities, activity)
}

func (turn *turnView) visibleActivities() []renderedActivity {
	toolCounts := make(map[string]int)
	for _, activity := range turn.activities {
		if activity.kind == "tool" {
			toolCounts[activity.executionID]++
		}
	}

	emittedSummaries := make(map[string]bool)
	visible := make([]renderedActivity, 0, len(turn.activities))
	for _, activity := range turn.activities {
		if activity.kind == "model" {
			continue
		}
		if activity.kind != "tool" || !turn.completedExecutions[activity.executionID] {
			visible = append(visible, activity)
			continue
		}
		if emittedSummaries[activity.executionID] {
			continue
		}
		emittedSummaries[activity.executionID] = true
		count := toolCounts[activity.executionID]
		label := fmt.Sprintf("called %d tools", count)
		if count == 1 {
			label = "called 1 tool"
		}
		visible = append(visible, newRenderedActivity(
			"tool-summary:"+activity.executionID,
			activity.executionID,
			activity.depth,
			"tool_summary",
			label,
			"",
		))
	}
	return visible
}

func loadSessionCmd(ctx context.Context, api *client, clearBlocks bool) tea.Cmd {
	return func() tea.Msg {
		return loadSession(ctx, api, clearBlocks)
	}
}

func loadSession(ctx context.Context, api *client, clearBlocks bool) sessionLoadedMsg {
	result := sessionLoadedMsg{api: api, clearBlocks: clearBlocks}
	if api.session == nil {
		return result
	}
	caller, err := api.getSession(ctx)
	if err != nil {
		api.logout(ctx)
		return result
	}
	api.caller = &caller
	result.caller = &caller

	user, err := api.getCurrentUser(ctx)
	if err != nil {
		result.startupError = fmt.Errorf("could not refresh your Sentinel profile: %w", err)
		return result
	}
	api.user = &user
	result.user = &user
	if !canUseBlixIdentity(&caller, &user) {
		result.startupError = errors.New(nonmemberAccessMessage)
		return result
	}

	created, err := createRunWithRetry(ctx, api)
	if err != nil {
		result.err = err
		return result
	}
	result.current = &created
	return result
}

func loginCmd(ctx context.Context, api *client) tea.Cmd {
	return func() tea.Msg {
		if _, err := api.login(ctx); err != nil {
			return sessionLoadedMsg{api: api, err: err}
		}
		return loadSession(ctx, api, true)
	}
}

func logoutCmd(ctx context.Context, api *client) tea.Cmd {
	return func() tea.Msg {
		api.logout(ctx)
		return sessionLoadedMsg{api: api, clearBlocks: true}
	}
}

func createRunCmd(ctx context.Context, api *client) tea.Cmd {
	return func() tea.Msg {
		created, err := api.createRun(ctx)
		return runCreatedMsg{current: created, err: err}
	}
}

func appendMessageCmd(ctx context.Context, api *client, turnID uint64, current createdRun, content string) tea.Cmd {
	return func() tea.Msg {
		result, err := api.appendMessage(ctx, current.Run.ID, content)
		return messageAppendedMsg{turnID: turnID, current: current, result: result, err: err}
	}
}

func queueMessageCmd(ctx context.Context, api *client, runID string, content string) tea.Cmd {
	return func() tea.Msg {
		_, err := api.appendMessage(ctx, runID, content)
		return queuedMessageMsg{content: content, err: err}
	}
}

func interruptRunCmd(ctx context.Context, api *client, runID string) tea.Cmd {
	return func() tea.Msg {
		return interruptedMsg{err: api.interruptRun(ctx, runID)}
	}
}

func pollEventsCmd(ctx context.Context, api *client, turnID uint64, runID string, after uint64) tea.Cmd {
	return func() tea.Msg {
		events, err := api.listEvents(ctx, runID, after)
		if err != nil {
			return executionEventsMsg{turnID: turnID, after: after, err: err}
		}
		if len(events) == 0 {
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return executionEventsMsg{turnID: turnID, after: after, err: ctx.Err()}
			case <-timer.C:
			}
		}
		return executionEventsMsg{turnID: turnID, after: after, events: events}
	}
}

func pollBackgroundEventsCmd(ctx context.Context, api *client, runID string, after uint64) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return backgroundEventsMsg{runID: runID, after: after, err: ctx.Err()}
		case <-timer.C:
		}
		events, err := api.listEvents(ctx, runID, after)
		return backgroundEventsMsg{runID: runID, after: after, events: events, err: err}
	}
}

func switchEndpointCmd(ctx context.Context, httpClient *http.Client, endpoint string) tea.Cmd {
	return func() tea.Msg {
		validationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := validateEndpoint(validationCtx, httpClient, endpoint)
		cancel()
		if err != nil {
			return sessionLoadedMsg{err: fmt.Errorf("could not connect to %s: %w", endpoint, err)}
		}
		stored, err := loadCredentials(endpoint)
		if err != nil {
			return sessionLoadedMsg{err: fmt.Errorf("could not load credentials for that endpoint: %w", err)}
		}
		candidate := &client{baseURL: endpoint, http: httpClient, session: stored}
		return loadSession(ctx, candidate, true)
	}
}

func formatDuration(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	return duration.Round(time.Second).String()
}

func renderUserMessage(content string, width int) string {
	message := lipgloss.NewStyle().
		Foreground(white).
		Background(userSurface).
		Render("❯  " + content)
	return renderMessageSurface(message, width, 1)
}

func renderMessageSurface(content string, width int, verticalPadding int) string {
	return lipgloss.NewStyle().
		Background(userSurface).
		Padding(verticalPadding, 2).
		MarginLeft(chatInset).
		Width(width).
		Render(content)
}

func renderAgentHeader(status string, width int) string {
	left := pinkStyle.Render("●  blix")
	right := mutedStyle.Render(status)
	return strings.Repeat(" ", chatInset) + alignedRow(left, right, width)
}

func renderActivityLine(activity renderedActivity, width int) string {
	if activity.kind == "tool_summary" {
		indent := strings.Repeat(" ", chatInset+activity.depth*4)
		return indent + lipgloss.NewStyle().Bold(true).Foreground(softPink).Render(activity.label)
	}

	status := strings.ToLower(strings.TrimSpace(activity.status))
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
	prefix := strings.Repeat(" ", activity.depth*4)
	kindColor := softPink
	if activity.kind == "agent" {
		kindColor = pink
	} else if activity.kind == "runtime" {
		kindColor = muted
	}
	kindLabel := lipgloss.NewStyle().Bold(true).Foreground(kindColor).Width(8).Render(activity.kind)
	labelWidth := width - lipgloss.Width(prefix) - lipgloss.Width(kindLabel) - lipgloss.Width(right) - 1
	label := truncateText(activity.label, labelWidth)
	left := lipgloss.JoinHorizontal(lipgloss.Top, prefix, kindLabel, mutedStyle.Render(label))
	return strings.Repeat(" ", chatInset) + alignedRow(left, right, width)
}

func renderResponse(content string, width int) string {
	return lipgloss.NewStyle().
		Padding(0, 2).
		MarginLeft(chatInset).
		Width(width).
		Render(renderMarkdown(content, max(20, width-4)))
}

func renderNoticePanel(content string, width int, borderColor color.Color) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Foreground(borderColor).
		Padding(0, 2).
		MarginLeft(chatInset).
		Width(width).
		Render(content)
}
