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
type conversationResumedMsg struct {
	current   createdConversation
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
			m.appendBlock(transcriptError, "Start a conversation first.", nil)
			return nil, true
		}
		id := m.current.Conversation.ID
		return func() tea.Msg {
			var response struct {
				Tasks []struct {
					ID        string `json:"task_id"`
					Status    string `json:"status"`
					Objective string `json:"objective"`
				} `json:"tasks"`
			}
			err := m.api.request(m.ctx, http.MethodGet, "/api/v1/conversations/"+id+"/tasks", nil, &response)
			var lines []string
			for _, task := range response.Tasks {
				lines = append(lines, fmt.Sprintf("%s  %s\n%s", task.ID, task.Status, task.Objective))
			}
			if len(lines) == 0 {
				lines = []string{"No tasks in this conversation."}
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
			m.appendBlock(transcriptError, "Usage: /resume <conversation_id> while idle", nil)
			return nil, true
		}
		return resumeConversationCmd(m.ctx, m.api, fields[1]), true
	case "/conversations":
		return func() tea.Msg {
			var response struct {
				Conversations []struct {
					ID   string `json:"conversation_id"`
					Name string `json:"name"`
				} `json:"conversations"`
			}
			err := m.api.request(m.ctx, http.MethodGet, "/api/v1/conversations", nil, &response)
			var lines []string
			for _, c := range response.Conversations {
				lines = append(lines, c.ID+"  "+c.Name)
			}
			return taskCommandMsg{content: strings.Join(lines, "\n"), err: err}
		}, true
	}
	return nil, false
}
func resumeConversationCmd(ctx context.Context, api *client, id string) tea.Cmd {
	return func() tea.Msg {
		var response struct {
			Conversation conversation      `json:"conversation"`
			Messages     []historyMessage  `json:"messages"`
			Approvals    []pendingApproval `json:"pending_approvals"`
		}
		err := api.request(ctx, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(id), nil, &response)
		var cursor uint64
		for _, message := range response.Messages {
			cursor = max(cursor, message.Position)
		}
		return conversationResumedMsg{current: createdConversation{Conversation: response.Conversation, Session: agentSession{ID: response.Conversation.LeadSessionID}}, messages: response.Messages, cursor: cursor, approvals: response.Approvals, err: err}
	}
}
