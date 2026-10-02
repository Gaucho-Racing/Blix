package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testUI() *tuiModel {
	m := newTUIModel(context.Background(), &client{})
	m.current = &createdConversation{Conversation: conversation{ID: "conversation_test", LeadSessionID: "session_lead"}, Session: agentSession{ID: "session_lead"}}
	m.activeTurnID = 1
	m.busy = true
	m.activeTurn = &turnView{backendTurnID: "turn_active", leadSessionID: "session_lead", tree: newActivityTree("session_lead"), toolIDs: map[string]string{}, completedExecutions: map[string]bool{}, activityIndex: map[string]int{}, startedAt: time.Now()}
	m.blocks = []transcriptBlock{{kind: transcriptTurn, turn: m.activeTurn}}
	return m
}
func TestInterleavedRepliesDoNotCompleteActiveTurn(t *testing.T) {
	m := testUI()
	m.handleExecutionEvents(executionEventsMsg{turnID: 1, events: []executionEvent{
		{Position: 1, TurnID: "turn_active", SessionID: "session_lead", Type: "model.output.delta_v1", Payload: json.RawMessage(`{"delta":"Current reply"}`)},
		{Position: 2, TurnID: "turn_background", SessionID: "session_lead", Type: "message.assistant_v1", Payload: json.RawMessage(`{"content":"Research finished"}`)},
		{Position: 3, TurnID: "turn_other", SessionID: "session_lead", Type: "attempt.failed_v1", Payload: json.RawMessage(`{"error":{"message":"Other turn failed"}}`)},
	}})
	if m.activeTurn == nil || !m.busy || m.activeTurn.response != "Current reply" {
		t.Fatalf("active turn changed: %+v", m.activeTurn)
	}
	if len(m.blocks) != 2 || m.blocks[1].turn.response != "Research finished" {
		t.Fatalf("background reply lost: %+v", m.blocks)
	}
	m.handleExecutionEvents(executionEventsMsg{turnID: 1, after: 3, events: []executionEvent{{Position: 4, TurnID: "turn_active", SessionID: "session_lead", Type: "message.assistant_v1", Payload: json.RawMessage(`{"content":"Current final"}`)}}})
	if m.activeTurn != nil || m.busy || m.blocks[0].turn.response != "Current final" {
		t.Fatal("active turn did not complete")
	}
}
func TestTaskCommandsUseNewEndpoints(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/v1/conversations/conversation_test/tasks" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"tasks":[{"task_id":"task_research","status":"running","objective":"Find evidence"}]}`))
			return
		}
		if r.URL.Path == "/api/v1/tasks/task_research/steer" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["content"] != "Focus on cost" {
				t.Errorf("guidance=%v %v", body, err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := testUI()
	m.api = &client{baseURL: server.URL, http: server.Client()}
	for _, text := range []string{"/tasks", "/steer task_research Focus on cost", "/cancel task_research", "/approve approval_publish", "/reject approval_publish"} {
		command, handled := m.handleTaskCommand(text)
		if !handled || command == nil {
			t.Fatalf("unhandled %s", text)
		}
		result := command().(taskCommandMsg)
		if result.err != nil {
			t.Fatal(result.err)
		}
		if text == "/tasks" && !strings.Contains(result.content, "task_research  running") {
			t.Fatal(result.content)
		}
	}
	if len(requests) != 5 || requests[3] != "POST /api/v1/approvals/approval_publish/approve" {
		t.Fatal(requests)
	}
}
func TestResumeRestoresConversationSessionAndCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"conversation":{"conversation_id":"conversation_test","lead_session_id":"session_lead"},"messages":[{"role":"user","content":"Hello","event_position":3},{"role":"assistant","content":"Hi","event_position":8}]}`))
	}))
	defer server.Close()
	result := resumeConversationCmd(context.Background(), &client{baseURL: server.URL, http: server.Client()}, "conversation_test")().(conversationResumedMsg)
	if result.err != nil || result.current.Session.ID != "session_lead" || result.cursor != 8 || len(result.messages) != 2 {
		t.Fatalf("resume=%+v", result)
	}
}

func TestNewQuestionPollsUnreadReplyBeforeItsInputPosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "10" {
			t.Errorf("poll skipped unread history: %s", r.URL.RawQuery)
			w.Write([]byte(`{"events":[]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[{"position":15,"turn_id":"turn_research","session_id":"session_lead","type":"message.assistant_v1","payload":{"content":"Completed research findings"}},{"position":20,"turn_id":"turn_active","session_id":"session_lead","type":"message.user_v1","payload":{"content":"Another question"}},{"position":21,"turn_id":"turn_active","session_id":"session_lead","type":"model.output.delta_v1","payload":{"delta":"New answer"}}]}`))
	}))
	defer server.Close()
	m := testUI()
	m.backgroundCursor = 10
	m.api = &client{baseURL: server.URL, http: server.Client()}
	appended := appendedMessage{EventPosition: 20}
	appended.Turn.ID = "turn_active"
	command := m.handleMessageAppended(messageAppendedMsg{turnID: 1, current: *m.current, result: appended})
	m.handleExecutionEvents(command().(executionEventsMsg))
	if len(m.blocks) != 2 || m.blocks[1].turn.response != "Completed research findings" {
		t.Fatal("unread research reply was lost")
	}
	if m.activeTurn == nil || m.activeTurn.response != "New answer" || m.backgroundCursor != 21 {
		t.Fatal("new question did not continue")
	}
}
func TestOverlappingPollDoesNotRedisplayConsumedReply(t *testing.T) {
	m := testUI()
	m.backgroundCursor = 10
	m.handleExecutionEvents(executionEventsMsg{turnID: 1, after: 0, events: []executionEvent{{Position: 5, TurnID: "turn_old", SessionID: "session_lead", Type: "message.assistant_v1", Payload: json.RawMessage(`{"content":"Already displayed"}`)}, {Position: 11, TurnID: "turn_active", SessionID: "session_lead", Type: "model.output.delta_v1", Payload: json.RawMessage(`{"delta":"Current"}`)}}})
	if len(m.blocks) != 1 || m.backgroundCursor != 11 || m.activeTurn.response != "Current" {
		t.Fatal("overlapping events were redisplayed")
	}
}
func TestOtherLeadTurnDoesNotClearActiveStream(t *testing.T) {
	m := testUI()
	m.activeTurn.response = "Current answer"
	m.handleExecutionEvents(executionEventsMsg{turnID: 1, events: []executionEvent{{Position: 1, TurnID: "turn_research", SessionID: "session_lead", Type: "model.call_v1", Payload: json.RawMessage(`{}`)}}})
	if m.activeTurn.response != "Current answer" {
		t.Fatal("another turn cleared the active response")
	}
}
