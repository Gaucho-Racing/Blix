package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/database"
	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixture struct {
	s            *ExecutionService
	db           *gorm.DB
	ctx          context.Context
	owner        model.CallerContext
	conversation CreatedConversation
	worker       string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("BLIX_TEST_DATABASE_PORT") == "" {
		t.Skip("BLIX_TEST_DATABASE_PORT is required for PostgreSQL integration tests")
	}
	cfg := config.Config{DatabaseHost: "127.0.0.1", DatabasePort: os.Getenv("BLIX_TEST_DATABASE_PORT"), DatabaseUser: "postgres", DatabasePassword: "blix-test", DatabaseName: "blix_test", ExecutionLeaseDuration: 30 * time.Second, WorkerHeartbeatInterval: 5 * time.Second, WorkerOfflineAfter: time.Minute, MQTTAttemptReadyTopic: "test/ready", MQTTConversationEventTopicPrefix: "test/events"}
	ctx := context.Background()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	if err := db.Exec("TRUNCATE runway_conversation, runway_agent_worker, runway_event, runway_outbox_message RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	workers := NewWorkerService(db, NewWorkerHub(), cfg)
	s := NewExecutionService(db, workers, cfg)
	f := &fixture{s: s, db: db, ctx: ctx, owner: model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", UserID: identifier.New(identifier.User)}, worker: identifier.New(identifier.Worker)}
	if _, err := workers.Register(ctx, WorkerRegistrationRequest{WorkerID: f.worker, Concurrency: 4, BackgroundConcurrency: 3}); err != nil {
		t.Fatal(err)
	}
	f.conversation, err = s.CreateConversation(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) turn(t *testing.T, text string) AttemptContext {
	t.Helper()
	msg, err := f.s.AppendUserMessage(f.ctx, f.conversation.Conversation.ID, f.owner.EntityID, text)
	if err != nil {
		t.Fatal(err)
	}
	var a model.ExecutionAttempt
	if err := f.db.First(&a, "turn_id = ? AND status = ?", msg.Turn.ID, model.StatusQueued).Error; err != nil {
		t.Fatal(err)
	}
	return f.claim(t, a.ID)
}
func (f *fixture) claim(t *testing.T, id string) AttemptContext {
	t.Helper()
	a, err := f.s.ClaimAttempt(f.ctx, id, ClaimRequest{WorkerID: f.worker, RuntimeKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func own(a AttemptContext) Ownership {
	return Ownership{WorkerID: a.Attempt.WorkerID, FencingToken: a.Attempt.FencingToken, ExpectedVersion: a.Attempt.Version}
}
func (f *fixture) delegate(t *testing.T, a AttemptContext, background bool) DelegatedTask {
	t.Helper()
	invocation := encoded(map[string]any{"schema_version": 1, "conversation_id": a.Conversation.ID, "session_id": identifier.New(identifier.Session), "parent_session_id": a.Session.ID, "agent_id": "deep_research_v1", "objective": "Research a question", "input": map[string]any{"question": "test"}})
	r := DelegationRequest{Ownership: own(a), RequestRef: "research-call", Background: background, Invocation: json.RawMessage(invocation)}
	if !background {
		r.Checkpoint = &CheckpointRequest{RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"opaque":"python-owned"}`)}
	}
	child, err := f.s.Delegate(f.ctx, a.Attempt.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	return child
}
func (f *fixture) taskAttempt(t *testing.T, id string) AttemptContext {
	t.Helper()
	var a model.ExecutionAttempt
	if err := f.db.First(&a, "task_id = ? AND status = ?", id, model.StatusQueued).Error; err != nil {
		t.Fatal(err)
	}
	return f.claim(t, a.ID)
}
func (f *fixture) completeTask(t *testing.T, a AttemptContext) {
	t.Helper()
	result := encoded(map[string]any{"schema_version": 1, "conversation_id": a.Conversation.ID, "session_id": a.Session.ID, "parent_session_id": a.Session.ParentSessionID, "agent_id": a.Session.ProfileID, "status": "succeeded", "output": map[string]any{"report": "verified"}, "usage": map[string]any{}})
	if err := f.s.Complete(f.ctx, a.Attempt.ID, CompletionRequest{Ownership: own(a), Result: json.RawMessage(result)}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundTaskOutlivesReplyAndInteractiveWork(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Research this in the background")
	child := f.delegate(t, lead, true)
	lead.Attempt.Version = child.ParentVersion
	if err := f.s.Complete(f.ctx, lead.Attempt.ID, CompletionRequest{Ownership: own(lead), Content: "Research started"}); err != nil {
		t.Fatal(err)
	}
	quick := f.turn(t, "What time is it?")
	research := f.taskAttempt(t, child.TaskID)
	f.completeTask(t, research)
	task, err := f.s.GetTask(f.ctx, child.TaskID)
	if err != nil || task.Status != model.StatusSucceeded {
		t.Fatalf("task: %+v %v", task, err)
	}
	var artifacts int64
	f.db.Model(&model.Artifact{}).Where("task_id = ?", child.TaskID).Count(&artifacts)
	if artifacts != 1 {
		t.Fatalf("artifacts=%d", artifacts)
	}
	if err := f.s.Complete(f.ctx, quick.Attempt.ID, CompletionRequest{Ownership: own(quick), Content: "Quick answer"}); err != nil {
		t.Fatal(err)
	}
	var wake model.ExecutionAttempt
	if err := f.db.Where("session_id = ? AND status = ?", lead.Session.ID, model.StatusQueued).First(&wake).Error; err != nil {
		t.Fatal(err)
	}
	resumed := f.claim(t, wake.ID)
	if len(resumed.Notifications) == 0 || string(resumed.Notifications) == "[]" {
		t.Fatal("missing completion notification")
	}
}
func TestBlockingDelegationAllowsAnotherTurn(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Research and answer")
	child := f.delegate(t, lead, false)
	quick := f.turn(t, "Unrelated quick question")
	if err := f.s.Complete(f.ctx, quick.Attempt.ID, CompletionRequest{Ownership: own(quick), Content: "Quick answer"}); err != nil {
		t.Fatal(err)
	}
	research := f.taskAttempt(t, child.TaskID)
	f.completeTask(t, research)
	var next model.ExecutionAttempt
	f.db.First(&next, "turn_id = ? AND status = ?", lead.Turn.ID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	if resumed.Checkpoint == nil || string(resumed.Checkpoint.Payload) != `{"opaque": "python-owned"}` {
		if resumed.Checkpoint == nil || !jsonEqual(resumed.Checkpoint.Payload, []byte(`{"opaque":"python-owned"}`)) {
			t.Fatal("lost opaque checkpoint")
		}
	}
	if len(resumed.ResumeResult) == 0 {
		t.Fatal("missing child result")
	}
}
func TestLeaseRecoveryFencesOldWorkerAndRetainsCheckpoint(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "hello")
	version, err := f.s.Checkpoint(f.ctx, lead.Attempt.ID, CheckpointRequest{Ownership: own(lead), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"any_runtime_state":{"cursor":17}}`)})
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = version
	f.db.Model(&model.ExecutionAttempt{}).Where("id = ?", lead.Attempt.ID).Update("lease_expires_at", time.Now().Add(-time.Minute))
	if err := f.s.RecoverExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Complete(f.ctx, lead.Attempt.ID, CompletionRequest{Ownership: own(lead), Content: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion: %v", err)
	}
	var next model.ExecutionAttempt
	f.db.First(&next, "target_id = ? AND status = ?", lead.Attempt.TargetID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	if resumed.Attempt.ID == lead.Attempt.ID || resumed.Checkpoint == nil || !jsonEqual(resumed.Checkpoint.Payload, []byte(`{"any_runtime_state":{"cursor":17}}`)) {
		t.Fatal("recovery lost identity or state")
	}
}
func TestApprovalBeforeSuspensionAndActionReplay(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "send a message")
	response, err := f.s.BeginAction(f.ctx, lead.Attempt.ID, ActionRequest{Ownership: own(lead), ToolID: "discord_webhook_send_v1", IdempotencyKey: "send", Arguments: json.RawMessage(`{"content":"hello"}`), RequiresApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = response.Version
	if response.Approval == nil {
		t.Fatal("no approval")
	}
	detail, err := f.s.GetConversation(f.ctx, f.conversation.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := detail["approval_actions"].([]model.Action)
	if len(actions) != 1 || actions[0].ToolID != "discord_webhook_send_v1" || !jsonEqual(actions[0].Arguments, response.Action.Arguments) {
		t.Fatalf("approval details=%+v", actions)
	}
	if err := f.s.DecideApproval(f.ctx, response.Approval.ID, f.owner.EntityID, "approve", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.WaitApproval(f.ctx, lead.Attempt.ID, response.Action.ID, CheckpointRequest{Ownership: own(lead), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"resume":true}`)}); err != nil {
		t.Fatal(err)
	}
	var next model.ExecutionAttempt
	f.db.First(&next, "target_id = ? AND status = ?", lead.Attempt.TargetID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	started, err := f.s.BeginAction(f.ctx, next.ID, ActionRequest{Ownership: own(resumed), ToolID: "discord_webhook_send_v1", IdempotencyKey: "send", Arguments: json.RawMessage(`{"content":"hello"}`), RequiresApproval: true})
	if err != nil || started.Action.Status != "running" {
		t.Fatalf("start: %+v %v", started, err)
	}
	resumed.Attempt.Version = started.Version
	v, err := f.s.FinishAction(f.ctx, next.ID, started.Action.ID, own(resumed), json.RawMessage(`{"status":"succeeded","output":{"id":"external-message"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resumed.Attempt.Version = v
	replay, err := f.s.BeginAction(f.ctx, next.ID, ActionRequest{Ownership: own(resumed), ToolID: "discord_webhook_send_v1", IdempotencyKey: "send", Arguments: json.RawMessage(`{"content":"hello"}`)})
	if err != nil || replay.Action.Status != "completed" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
}
func TestCompletedSuspendedAttemptCannotFailFinishedTurn(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "research")
	child := f.delegate(t, lead, false)
	research := f.taskAttempt(t, child.TaskID)
	f.completeTask(t, research)
	var next model.ExecutionAttempt
	f.db.First(&next, "target_id = ? AND status = ?", lead.Attempt.TargetID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	if err := f.s.Complete(f.ctx, next.ID, CompletionRequest{Ownership: own(resumed), Content: "done"}); err != nil {
		t.Fatal(err)
	}
	f.db.Model(&model.ExecutionAttempt{}).Where("id = ?", lead.Attempt.ID).Update("deadline_at", time.Now().Add(-time.Minute))
	if err := f.s.RecoverExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	var turn model.Turn
	f.db.First(&turn, "id = ?", lead.Turn.ID)
	if turn.Status != model.StatusSucceeded {
		t.Fatalf("finished turn became %s", turn.Status)
	}
}
func TestCancelBlockingTaskResumesParentWithResult(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "research")
	child := f.delegate(t, lead, false)
	if err := f.s.ControlTask(f.ctx, child.TaskID, f.owner.EntityID, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	var next model.ExecutionAttempt
	f.db.First(&next, "target_id = ? AND status = ?", lead.Attempt.TargetID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	if len(resumed.ResumeResult) == 0 {
		t.Fatal("cancelled child did not resume parent")
	}
}

func TestQueuedTurnIncludesPrecedingReply(t *testing.T) {
	f := setup(t)
	first := f.turn(t, "First question")
	second, err := f.s.AppendUserMessage(f.ctx, first.Conversation.ID, f.owner.EntityID, "Second question")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Complete(f.ctx, first.Attempt.ID, CompletionRequest{Ownership: own(first), Content: "First answer"}); err != nil {
		t.Fatal(err)
	}
	var attempt model.ExecutionAttempt
	f.db.First(&attempt, "turn_id = ? AND status = ?", second.Turn.ID, model.StatusQueued)
	next := f.claim(t, attempt.ID)
	if len(next.Messages) != 3 || next.Messages[1].Content != "Second question" || next.Messages[2].Content != "First answer" {
		t.Fatalf("history: %+v", next.Messages)
	}
}
func TestConcurrentClaimsHaveOneOwner(t *testing.T) {
	f := setup(t)
	message, err := f.s.AppendUserMessage(f.ctx, f.conversation.Conversation.ID, f.owner.EntityID, "Question")
	if err != nil {
		t.Fatal(err)
	}
	var attempt model.ExecutionAttempt
	f.db.First(&attempt, "turn_id = ?", message.Turn.ID)
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := f.s.ClaimAttempt(f.ctx, attempt.ID, ClaimRequest{WorkerID: f.worker, RuntimeKind: "api"})
			results <- err
		}()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("owners=%d", successes)
	}
}
func TestTaskControlsAreScopedAndCommandsAreDurable(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Start work")
	child := f.delegate(t, lead, true)
	lead.Attempt.Version = child.ParentVersion
	listed, err := f.s.SessionTaskControl(f.ctx, lead.Attempt.ID, TaskControlRequest{Ownership: own(lead), Operation: "list"})
	if err != nil || len(listed["tasks"].([]model.Task)) != 1 {
		t.Fatalf("list=%v %v", listed, err)
	}
	steered, err := f.s.SessionTaskControl(f.ctx, lead.Attempt.ID, TaskControlRequest{Ownership: own(lead), Operation: "steer", TaskID: child.TaskID, Content: "Focus on cost"})
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = steered["version"].(uint64)
	research := f.taskAttempt(t, child.TaskID)
	if len(research.Commands) != 1 || research.Commands[0].Kind != "steer" {
		t.Fatalf("commands=%v", research.Commands)
	}
	var commands []model.SessionCommand
	version, err := f.s.Checkpoint(f.ctx, research.Attempt.ID, CheckpointRequest{Ownership: own(research), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"opaque":true}`), CommandIDs: []string{research.Commands[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	research.Attempt.Version = version
	commands, err = f.s.Commands(f.ctx, research.Attempt.ID, own(research))
	if err != nil || len(commands) != 0 {
		t.Fatalf("ack=%v %v", commands, err)
	}
	_, err = f.s.SessionTaskControl(f.ctx, research.Attempt.ID, TaskControlRequest{Ownership: own(research), Operation: "list"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing capability was accepted: %v", err)
	}
}
func TestConsumedResumeIsNotReplayedAfterRecovery(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Research")
	child := f.delegate(t, lead, false)
	f.completeTask(t, f.taskAttempt(t, child.TaskID))
	var next model.ExecutionAttempt
	f.db.First(&next, "turn_id = ? AND status = ?", lead.Turn.ID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	version, err := f.s.Checkpoint(f.ctx, resumed.Attempt.ID, CheckpointRequest{Ownership: own(resumed), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"phase":"ready"}`), ResumeConsumed: true})
	if err != nil {
		t.Fatal(err)
	}
	resumed.Attempt.Version = version
	if err := f.s.Fail(f.ctx, resumed.Attempt.ID, FailureRequest{Ownership: own(resumed), Error: json.RawMessage(`{"code":"temporary","message":"retry","retryable":true}`), Retryable: true}); err != nil {
		t.Fatal(err)
	}
	next = model.ExecutionAttempt{}
	f.db.First(&next, "turn_id = ? AND status = ?", lead.Turn.ID, model.StatusQueued)
	retry := f.claim(t, next.ID)
	if len(retry.ResumeResult) != 0 {
		t.Fatalf("result was replayed: %s", retry.ResumeResult)
	}
}
func TestFailedTaskProducesContractError(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Start")
	child := f.delegate(t, lead, false)
	research := f.taskAttempt(t, child.TaskID)
	result := encoded(map[string]any{"conversation_id": research.Conversation.ID, "session_id": research.Session.ID, "parent_session_id": lead.Session.ID, "agent_id": "deep_research_v1", "status": "failed", "error": map[string]any{"code": "no_evidence", "message": "No evidence"}})
	if err := f.s.Complete(f.ctx, research.Attempt.ID, CompletionRequest{Ownership: own(research), Result: json.RawMessage(result)}); err != nil {
		t.Fatal(err)
	}
	task, err := f.s.GetTask(f.ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(task.Result, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "no_evidence" {
		t.Fatalf("result=%s", task.Result)
	}
}
func TestPythonClaimContract(t *testing.T) {
	f := setup(t)
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv is required for the Python contract test")
	}
	lead := f.turn(t, "Test contract")
	child := f.delegate(t, lead, true)
	lead.Attempt.Version = child.ParentVersion
	research := f.taskAttempt(t, child.TaskID)
	payload, err := json.Marshal([]AttemptContext{lead, research})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("uv", "run", "--project", "../../agent-worker", "--no-sync", "python", "-c", `import json,sys; from blix_agent_worker.clients.runway import ExecutionContext; contexts=[ExecutionContext.model_validate(c) for c in json.load(sys.stdin)]; assert contexts[0].profile.profile_id == 'blix_v1'; assert contexts[1].profile.profile_id == 'deep_research_v1'`)
	command.Stdin = bytes.NewReader(payload)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Python contract: %v\n%s", err, output)
	}
}

func TestBackgroundSaturationPreservesInteractiveSlot(t *testing.T) {
	f := setup(t)
	if _, err := f.s.workers.Register(f.ctx, WorkerRegistrationRequest{WorkerID: f.worker, Concurrency: 2, BackgroundConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	first := f.turn(t, "Research first")
	child := f.delegate(t, first, true)
	first.Attempt.Version = child.ParentVersion
	if err := f.s.Complete(f.ctx, first.Attempt.ID, CompletionRequest{Ownership: own(first), Content: "Started"}); err != nil {
		t.Fatal(err)
	}
	f.taskAttempt(t, child.TaskID)
	second := f.turn(t, "Research second")
	blocked := f.delegate(t, second, true)
	second.Attempt.Version = blocked.ParentVersion
	if err := f.s.Complete(f.ctx, second.Attempt.ID, CompletionRequest{Ownership: own(second), Content: "Queued"}); err != nil {
		t.Fatal(err)
	}
	var a model.ExecutionAttempt
	f.db.First(&a, "task_id = ? AND status = ?", blocked.TaskID, model.StatusQueued)
	if _, err := f.s.ClaimAttempt(f.ctx, a.ID, ClaimRequest{WorkerID: f.worker, RuntimeKind: "api"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("background capacity exceeded: %v", err)
	}
	quick := f.turn(t, "Quick question")
	if quick.Attempt.Queue != "interactive" {
		t.Fatal("quick turn went to background queue")
	}
}
func TestRejectBeforeSuspensionResumesWithoutExecution(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Approve a write")
	action, err := f.s.BeginAction(f.ctx, lead.Attempt.ID, ActionRequest{Ownership: own(lead), ToolID: "knowledge_remember_v1", IdempotencyKey: "write", Arguments: json.RawMessage(`{}`), RequiresApproval: true, Idempotent: true})
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = action.Version
	if err := f.s.DecideApproval(f.ctx, action.Approval.ID, f.owner.EntityID, "reject", "Do not write"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.WaitApproval(f.ctx, lead.Attempt.ID, action.Action.ID, CheckpointRequest{Ownership: own(lead), RuntimeKind: "api", SchemaVersion: 1, Payload: json.RawMessage(`{"opaque":true}`)}); err != nil {
		t.Fatal(err)
	}
	var next model.ExecutionAttempt
	f.db.First(&next, "turn_id = ? AND status = ?", lead.Turn.ID, model.StatusQueued)
	resumed := f.claim(t, next.ID)
	denied, err := f.s.BeginAction(f.ctx, resumed.Attempt.ID, ActionRequest{Ownership: own(resumed), ToolID: "knowledge_remember_v1", IdempotencyKey: "write", Arguments: json.RawMessage(`{}`), RequiresApproval: true, Idempotent: true})
	if err != nil || denied.Action.Status != "denied" {
		t.Fatalf("denied=%+v %v", denied, err)
	}
}
func TestCancelledQueuedAttemptsStopDispatching(t *testing.T) {
	f := setup(t)
	message, err := f.s.AppendUserMessage(f.ctx, f.conversation.Conversation.ID, f.owner.EntityID, "Cancel before claim")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.InterruptConversation(f.ctx, f.conversation.Conversation.ID, f.owner.EntityID); err != nil {
		t.Fatal(err)
	}
	var pending int64
	f.db.Model(&model.OutboxMessage{}).Where("attempt_id IN (SELECT id FROM runway_execution_attempt WHERE turn_id = ?) AND acknowledge_on_publish = false AND acknowledged_at IS NULL", message.Turn.ID).Count(&pending)
	if pending != 0 {
		t.Fatal("cancelled attempt still dispatches")
	}
}

func TestRuntimeCannotOverrideNonIdempotentActionPolicy(t *testing.T) {
	f := setup(t)
	lead := f.turn(t, "Send a message")
	request := ActionRequest{Ownership: own(lead), ToolID: "discord_webhook_send_v1", IdempotencyKey: "discord-send", Arguments: json.RawMessage(`{"channel_id":"123","content":"Hello"}`), Idempotent: true}
	action, err := f.s.BeginAction(f.ctx, lead.Attempt.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	lead.Attempt.Version = action.Version
	request.Ownership = own(lead)
	if _, err := f.s.BeginAction(f.ctx, lead.Attempt.ID, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsafe replay allowed: %v", err)
	}
}

func TestDocumentRequestDisplayPreservesAgentInput(t *testing.T) {
	f := setup(t)
	original := "Curate source records in propose mode with complete original provenance."
	display := "Summarize the brake rules from PDF pages 65–66."
	appended, err := f.s.AppendUserMessageWithDisplay(f.ctx, f.conversation.Conversation.ID, f.owner.EntityID, original, display)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.GetConversation(f.ctx, f.conversation.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages := detail["messages"].([]model.Message)
	if len(messages) != 1 || messages[0].Content != original || messages[0].DisplayContent != display {
		t.Fatalf("message=%+v", messages)
	}
	if detail["conversation"].(model.Conversation).Name != display {
		t.Fatal("conversation title used internal instructions")
	}
	var attempt model.ExecutionAttempt
	if err := f.db.First(&attempt, "turn_id = ?", appended.Turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	claimed := f.claim(t, attempt.ID)
	if len(claimed.Messages) != 1 || claimed.Messages[0].Content != original {
		t.Fatalf("agent input=%+v", claimed.Messages)
	}
}
