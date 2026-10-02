package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Open(ctx context.Context, configuration config.Config) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable TimeZone=UTC",
		configuration.DatabaseHost,
		configuration.DatabaseUser,
		configuration.DatabasePassword,
		configuration.DatabaseName,
		configuration.DatabasePort,
	)
	var database *gorm.DB
	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		database, err = gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := database.AutoMigrate(
		&model.Run{}, &model.AgentSession{}, &model.Turn{}, &model.Task{}, &model.ExecutionAttempt{},
		&model.RuntimeCheckpoint{}, &model.Message{}, &model.Event{}, &model.AgentWorker{}, &model.OutboxMessage{},
		&model.Workspace{}, &model.WorkspaceRepository{}, &model.Artifact{}, &model.Action{}, &model.CapabilityGrant{},
		&model.Approval{}, &model.SessionCommand{}, &model.TurnTask{},
	); err != nil {
		return nil, fmt.Errorf("create database schema: %w", err)
	}
	constraints := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_attempt_number ON runway_execution_attempt (target_id,number)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_running_session ON runway_execution_attempt (session_id) WHERE status = 'running'",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_live_target ON runway_execution_attempt (target_id) WHERE status IN ('queued','running')",
		"CREATE INDEX IF NOT EXISTS idx_pending_outbox ON runway_outbox_message (created_at) WHERE acknowledged_at IS NULL",
		"CREATE INDEX IF NOT EXISTS idx_run_events ON runway_event (run_id,position)",
		"CREATE INDEX IF NOT EXISTS idx_queued_session ON runway_execution_attempt (session_id,created_at) WHERE status = 'queued'",
		"ALTER TABLE runway_execution_attempt ADD CONSTRAINT chk_attempt_target CHECK ((turn_id IS NOT NULL)::int + (task_id IS NOT NULL)::int = 1 AND target_id = COALESCE(turn_id,task_id))",
	}
	for _, statement := range constraints {
		if strings.HasPrefix(statement, "ALTER TABLE") {
			var count int64
			if err := database.Raw("SELECT count(*) FROM pg_constraint WHERE conname = 'chk_attempt_target'").Scan(&count).Error; err != nil {
				return nil, err
			}
			if count > 0 {
				continue
			}
		}
		if err := database.Exec(statement).Error; err != nil {
			return nil, fmt.Errorf("create database constraint: %w", err)
		}
	}
	if err := createRelations(database); err != nil {
		return nil, err
	}
	return database, nil
}

func createRelations(db *gorm.DB) error {
	relations := []struct{ table, column, target string }{
		{"runway_agent_session", "run_id", "runway_run"},
		{"runway_agent_session", "parent_session_id", "runway_agent_session"},
		{"runway_run", "lead_session_id", "runway_agent_session"},
		{"runway_turn", "run_id", "runway_run"},
		{"runway_turn", "session_id", "runway_agent_session"},
		{"runway_task", "run_id", "runway_run"},
		{"runway_task", "session_id", "runway_agent_session"},
		{"runway_task", "parent_session_id", "runway_agent_session"},
		{"runway_task", "parent_task_id", "runway_task"},
		{"runway_task", "origin_turn_id", "runway_turn"},
		{"runway_execution_attempt", "run_id", "runway_run"},
		{"runway_execution_attempt", "session_id", "runway_agent_session"},
		{"runway_execution_attempt", "turn_id", "runway_turn"},
		{"runway_execution_attempt", "task_id", "runway_task"},
		{"runway_message", "run_id", "runway_run"},
		{"runway_message", "turn_id", "runway_turn"},
		{"runway_message", "session_id", "runway_agent_session"},
		{"runway_turn", "input_message_id", "runway_message"},
		{"runway_turn", "trigger_task_id", "runway_task"},
		{"runway_turn", "waiting_task_id", "runway_task"},
		{"runway_task", "waiting_task_id", "runway_task"},
		{"runway_agent_session", "workspace_id", "runway_workspace"},
		{"runway_event", "run_id", "runway_run"},
		{"runway_event", "session_id", "runway_agent_session"},
		{"runway_event", "turn_id", "runway_turn"},
		{"runway_event", "task_id", "runway_task"},
		{"runway_event", "attempt_id", "runway_execution_attempt"},
		{"runway_action", "run_id", "runway_run"},
		{"runway_action", "attempt_id", "runway_execution_attempt"},
		{"runway_approval", "run_id", "runway_run"},
		{"runway_approval", "requested_by_session_id", "runway_agent_session"},
		{"runway_capability_grant", "run_id", "runway_run"},
		{"runway_artifact", "run_id", "runway_run"},
		{"runway_runtime_checkpoint", "session_id", "runway_agent_session"},
		{"runway_workspace", "run_id", "runway_run"},
		{"runway_workspace_repository", "workspace_id", "runway_workspace"},
		{"runway_artifact", "task_id", "runway_task"},
		{"runway_artifact", "attempt_id", "runway_execution_attempt"},
		{"runway_action", "session_id", "runway_agent_session"},
		{"runway_capability_grant", "session_id", "runway_agent_session"},
		{"runway_approval", "action_id", "runway_action"},
		{"runway_session_command", "session_id", "runway_agent_session"},
		{"runway_turn_task", "turn_id", "runway_turn"},
		{"runway_turn_task", "task_id", "runway_task"},
	}
	for _, r := range relations {
		name := "fk_" + r.table + "_" + r.column
		var count int64
		if err := db.Raw("SELECT count(*) FROM pg_constraint WHERE conname = ?", name).Scan(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(id) DEFERRABLE INITIALLY DEFERRED", r.table, name, r.column, r.target)
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create relation %s: %w", name, err)
		}
	}
	return nil
}
