package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	err       error
}
type pendingApproval struct {
	ID       string `json:"approval_id"`
	ActionID string `json:"action_id"`
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
		m.appendBlock(transcriptNotice, fmt.Sprintf("Approval required: %s\n%s\n/approve %s  or  /reject %s", payload.ToolID, payload.Arguments, payload.ApprovalID, payload.ApprovalID), nil)
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
	case "/cancel", "/steer", "/approve", "/reject":
		if len(fields) < 2 || (fields[0] == "/steer" && len(fields) < 3) {
			m.appendBlock(transcriptError, "Usage: /cancel <task_id>, /steer <task_id> <guidance>, /approve <approval_id>, /reject <approval_id> [reason]", nil)
			return nil, true
		}
		operation := strings.TrimPrefix(fields[0], "/")
		id := url.PathEscape(fields[1])
		payload := map[string]string{}
		path := "/api/v1/tasks/" + id + "/" + operation
		if operation == "steer" {
			payload["content"] = strings.Join(fields[2:], " ")
		}
		if operation == "approve" || operation == "reject" {
			path = "/api/v1/approvals/" + id + "/" + operation
			payload["note"] = strings.Join(fields[2:], " ")
		}
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
		}
		err := api.request(ctx, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id), nil, &response)
		var cursor uint64
		for _, message := range response.Messages {
			cursor = max(cursor, message.Position)
		}
		return runResumedMsg{current: createdRun{Run: response.Run, Session: agentSession{ID: response.Run.LeadSessionID}}, messages: response.Messages, cursor: cursor, approvals: response.Approvals, err: err}
	}
}
