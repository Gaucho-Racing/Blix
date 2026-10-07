package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

func TestTaskQuestionAndAnswerValidation(t *testing.T) {
	question, err := parseTaskQuestion(json.RawMessage(`{"id":"ui_1","questions":[{"id":"scope","question":"Which scope?","options":[{"label":"Minimal"},{"label":"Broad"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range [][]taskAnswer{
		{{ID: "scope", SelectedOptions: []string{"Minimal"}}},
		{{ID: "scope", Text: "Keep the current API"}},
	} {
		if err := validateTaskAnswers(question, answer); err != nil {
			t.Fatal(err)
		}
	}
	for _, answer := range [][]taskAnswer{
		nil,
		{{ID: "other", Text: "Minimal"}},
		{{ID: "scope"}},
		{{ID: "scope", SelectedOptions: []string{"Unknown"}}},
		{{ID: "scope", SelectedOptions: []string{"Minimal", "Minimal"}}},
		{{ID: "scope", SelectedOptions: []string{"Minimal", "Broad"}}},
		{{ID: "scope", SelectedOptions: []string{"Minimal"}, Text: "Both"}},
	} {
		if !errors.Is(validateTaskAnswers(question, answer), ErrInvalid) {
			t.Fatalf("accepted invalid answers: %+v", answer)
		}
	}
	for _, payload := range []string{
		`{"id":"ui_1","questions":[]}`,
		`{"id":"ui_1","questions":[{"id":"scope","question":"One"},{"id":"scope","question":"Two"}]}`,
		`{"id":"ui_1","questions":[{"id":"scope","question":"One","options":[{"label":"Same"},{"label":"Same"}]}]}`,
	} {
		if _, err := parseTaskQuestion(json.RawMessage(payload)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid question: %s", payload)
		}
	}
}

func TestSpecialistQuestionsNotifyLeadAndResumeOnce(t *testing.T) {
	for _, agentID := range []string{"blix_code_v1", "deep_research_v1", "knowledge_curator_v1"} {
		t.Run(agentID, func(t *testing.T) { testSpecialistQuestion(t, agentID) })
	}
}

func testSpecialistQuestion(t *testing.T, agentID string) {
	f := setup(t)
	lead := f.turn(t, "Fix a bug")
	invocation := encoded(map[string]any{"schema_version": 1, "run_id": f.run.Run.ID,
		"session_id": identifier.New(identifier.Session), "parent_session_id": lead.Session.ID,
		"agent_id": agentID, "objective": "Fix a bug", "input": map[string]any{"repository": "team/repo"}})
	child, err := f.s.Delegate(f.ctx, lead.Attempt.ID, DelegationRequest{Ownership: own(lead), RequestRef: "coding", Background: true, Invocation: json.RawMessage(invocation)})
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = child.ParentVersion
	if err := f.s.Complete(f.ctx, lead.Attempt.ID, CompletionRequest{Ownership: own(lead), Content: "Started"}); err != nil {
		t.Fatal(err)
	}
	coding := f.taskAttempt(t, child.TaskID)
	question := json.RawMessage(`{"id":"ui_1","questions":[{"id":"scope","question":"Which scope?","options":[{"label":"Minimal"}]}]}`)
	if agentID == "blix_code_v1" {
		for range 2 {
			version, err := f.s.RecordEvents(f.ctx, coding.Attempt.ID, own(coding), []RuntimeEvent{{Type: "runtime.question_v1", EntityID: coding.Session.ID, Payload: question}})
			if err != nil {
				t.Fatal(err)
			}
			coding.Attempt.Version = version
		}
	} else {
		action, err := f.s.BeginAction(f.ctx, coding.Attempt.ID, ActionRequest{Ownership: own(coding), ToolID: "agent_ask_v1", IdempotencyKey: "ask", Arguments: json.RawMessage(`{"questions":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		coding.Attempt.Version = action.Version
		_, err = f.s.WaitTaskInput(f.ctx, coding.Attempt.ID, TaskInputRequest{CheckpointRequest: CheckpointRequest{Ownership: own(coding), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"phase":"processing_calls"}`)}, Question: question})
		if err != nil {
			t.Fatal(err)
		}
		var suspended model.ExecutionAttempt
		if err := f.db.First(&suspended, "id = ?", coding.Attempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		if suspended.Status != model.StatusSuspended || suspended.LeaseExpiresAt != nil {
			t.Fatal("waiting specialist retained its worker lease")
		}
	}
	var turns []model.Turn
	if err := f.db.Where("run_id = ? AND status = ?", f.run.Run.ID, model.StatusQueued).Find(&turns).Error; err != nil || len(turns) != 1 {
		t.Fatalf("duplicate question notifications: %v %v", turns, err)
	}
	var attempt model.ExecutionAttempt
	if err := f.db.First(&attempt, "turn_id = ?", turns[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	reply := f.claim(t, attempt.ID)
	assertDelivery := func(expected string) {
		t.Helper()
		status, err := f.s.SessionTaskControl(f.ctx, reply.Attempt.ID, TaskControlRequest{Ownership: own(reply), Operation: "status", TaskID: child.TaskID})
		if err != nil {
			t.Fatal(err)
		}
		delivery, ok := status["answer_delivery"].(map[string]any)
		if !ok || delivery["status"] != expected || delivery["question_id"] != "ui_1" {
			t.Fatalf("unexpected answer delivery: %v", delivery)
		}
	}

	var notifications []map[string]any
	if err := json.Unmarshal(reply.Notifications, &notifications); err != nil || len(notifications) != 1 || notifications[0]["type"] != "task.question_v1" {
		t.Fatalf("missing question notification: %s", reply.Notifications)
	}
	request := TaskControlRequest{Ownership: own(reply), Operation: "answer", TaskID: child.TaskID,
		QuestionID: "old", Answers: json.RawMessage(`[{"id":"scope","selected_options":["Minimal"],"text":""}]`)}
	if _, err := f.s.SessionTaskControl(f.ctx, reply.Attempt.ID, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale answer accepted: %v", err)
	}
	request.QuestionID = "ui_1"
	for range 2 {
		request.Ownership = own(reply)
		result, err := f.s.SessionTaskControl(f.ctx, reply.Attempt.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		reply.Attempt.Version = result["version"].(uint64)
	}
	assertDelivery("queued")
	if agentID != "blix_code_v1" {
		var attempts int64
		if err := f.db.Model(&model.ExecutionAttempt{}).Where("target_id = ? AND status = ?", child.TaskID, model.StatusQueued).Count(&attempts).Error; err != nil || attempts != 1 {
			t.Fatalf("duplicate answers created multiple resumptions: %d %v", attempts, err)
		}
		coding = f.taskAttempt(t, child.TaskID)
		if coding.Checkpoint == nil || !jsonEqual(coding.Checkpoint.Payload, json.RawMessage(`{"phase":"processing_calls"}`)) {
			t.Fatal("specialist checkpoint was lost")
		}
		action, err := f.s.BeginAction(f.ctx, coding.Attempt.ID, ActionRequest{Ownership: own(coding), ToolID: "agent_ask_v1", IdempotencyKey: "ask", Arguments: json.RawMessage(`{"questions":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		coding.Attempt.Version = action.Version
	}
	commands, err := f.s.Commands(f.ctx, coding.Attempt.ID, own(coding))
	if err != nil || len(commands) != 1 || commands[0].Kind != "answer" {
		t.Fatalf("answer command not delivered exactly once: %v %v", commands, err)
	}
	version, err := f.s.AcknowledgeCommands(f.ctx, coding.Attempt.ID, own(coding), []string{commands[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	coding.Attempt.Version = version
	if agentID == "blix_code_v1" {
		assertDelivery("delivered_to_sandbox")
	} else {
		assertDelivery("delivered_to_agent")
	}
	commands, err = f.s.Commands(f.ctx, coding.Attempt.ID, own(coding))
	if err != nil || len(commands) != 0 {
		t.Fatalf("answer replayed after acknowledgment: %v %v", commands, err)
	}
	if err := f.s.Complete(f.ctx, reply.Attempt.ID, CompletionRequest{Ownership: own(reply), Content: "Answered"}); err != nil {
		t.Fatal(err)
	}
	f.completeTask(t, coding)
	var completion model.Turn
	if err := f.db.First(&completion, "trigger_task_id = ?", child.TaskID).Error; err != nil {
		t.Fatalf("question notification prevented completion notification: %v", err)
	}
}
