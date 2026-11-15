package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type taskCommandMsg struct {
	content string
	err     error
}
type runResumedMsg struct {
	current   createdRun
	messages  []historyMessage
	cursor    uint64
	approvals []pendingApproval
	actions   []approvalAction
	err       error
}
type pendingApproval struct {
	ID       string `json:"approval_id"`
	ActionID string `json:"action_id"`
}
type approvalAction struct {
	ID        string          `json:"action_id"`
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
}

const maxApprovalArgumentChars = 1200

func approvalNotice(approvalID string, toolID string, arguments json.RawMessage) string {
	if toolID == "" {
		toolID = "unknown tool"
	}
	lines := []string{"Approval required  " + toolID}
	var decoded map[string]any
	if json.Unmarshal(arguments, &decoded) == nil && len(decoded) > 0 {
		keys := make([]string, 0, len(decoded))
		for key := range decoded {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, _ := json.Marshal(decoded[key])
			if text, ok := decoded[key].(string); ok {
				value = []byte(text)
			}
			lines = append(lines, "  "+key+": "+singleLine(string(value), 160))
		}
	} else if len(arguments) > 0 {
		lines = append(lines, "  "+singleLine(string(arguments), maxApprovalArgumentChars))
	}
	lines = append(lines, "/approve "+approvalID+"  or  /reject "+approvalID+" [reason]")
	return strings.Join(lines, "\n")
}

func (m *tuiModel) showApprovalDecision(event executionEvent) {
	var payload approvalDecidedPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.ApprovalID == "" {
		return
	}
	kind := transcriptNotice
	if payload.Status != "approved" {
		kind = transcriptError
	}
	m.appendBlock(kind, "Approval "+payload.ApprovalID+" "+payload.Status, nil)
}

type historyMessage struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	Position uint64 `json:"event_position"`
}

func (m *tuiModel) showApproval(event executionEvent) {
	var payload struct {
		ApprovalID string          `json:"approval_id"`
		ToolID     string          `json:"tool_id"`
		Arguments  json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(event.Payload, &payload) == nil {
		m.appendBlock(transcriptNotice, approvalNotice(payload.ApprovalID, payload.ToolID, payload.Arguments), nil)
	}
}
func (m *tuiModel) handleTaskCommand(command string) (tea.Cmd, bool) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, false
	}
	switch fields[0] {
	case "/tasks":
		if m.current == nil {
			m.appendBlock(transcriptError, "Start a run first.", nil)
			return nil, true
		}
		id := m.current.Run.ID
		return func() tea.Msg {
			var response struct {
				Tasks []struct {
					ID        string `json:"task_id"`
					Status    string `json:"status"`
					Objective string `json:"objective"`
				} `json:"tasks"`
			}
			err := m.api.request(m.ctx, http.MethodGet, "/api/v1/runs/"+id+"/tasks", nil, &response)
			var lines []string
			for _, task := range response.Tasks {
				lines = append(lines, fmt.Sprintf("%s  %s\n%s", task.ID, task.Status, task.Objective))
			}
			if len(lines) == 0 {
				lines = []string{"No tasks in this run."}
			}
			return taskCommandMsg{content: strings.Join(lines, "\n\n"), err: err}
		}, true
	case "/approve", "/reject":
		if len(fields) < 2 {
			m.appendBlock(transcriptError, "Usage: /approve <approval_id>, /reject <approval_id> [reason]", nil)
			return nil, true
		}
		operation := strings.TrimPrefix(fields[0], "/")
		path := "/api/v1/approvals/" + url.PathEscape(fields[1]) + "/" + operation
		payload := map[string]string{"note": strings.Join(fields[2:], " ")}
		return func() tea.Msg {
			err := m.api.request(m.ctx, http.MethodPost, path, payload, nil)
			return taskCommandMsg{content: operation + " accepted for " + fields[1], err: err}
		}, true
	case "/resume":
		if len(fields) != 2 || m.busy {
			m.appendBlock(transcriptError, "Usage: /resume <run_id> while idle", nil)
			return nil, true
		}
		return resumeRunCmd(m.ctx, m.api, fields[1]), true
	case "/runs":
		return func() tea.Msg {
			var response struct {
				Runs []struct {
					ID   string `json:"run_id"`
					Name string `json:"name"`
				} `json:"runs"`
			}
			err := m.api.request(m.ctx, http.MethodGet, "/api/v1/runs?kind=chat", nil, &response)
			var lines []string
			for _, c := range response.Runs {
				lines = append(lines, c.ID+"  "+c.Name)
			}
			return taskCommandMsg{content: strings.Join(lines, "\n"), err: err}
		}, true
	}
	return nil, false
}
func resumeRunCmd(ctx context.Context, api *client, id string) tea.Cmd {
	return func() tea.Msg {
		var response struct {
			Run       run               `json:"run"`
			Messages  []historyMessage  `json:"messages"`
			Approvals []pendingApproval `json:"pending_approvals"`
			Actions   []approvalAction  `json:"approval_actions"`
		}
		err := api.request(ctx, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id), nil, &response)
		var cursor uint64
		for _, message := range response.Messages {
			cursor = max(cursor, message.Position)
		}
		return runResumedMsg{current: createdRun{Run: response.Run, Session: agentSession{ID: response.Run.LeadSessionID}}, messages: response.Messages, cursor: cursor, approvals: response.Approvals, actions: response.Actions, err: err}
	}
}
