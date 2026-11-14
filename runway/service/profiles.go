package service

import "github.com/gaucho-racing/blix/runway/model"

type AgentProfile struct {
	ID          string                `json:"profile_id"`
	Version     uint64                `json:"version"`
	RuntimeKind string                `json:"runtime_kind"`
	Tools       []string              `json:"tool_ids"`
	Executors   []string              `json:"executor_ids,omitempty"`
	Policy      model.ExecutionPolicy `json:"policy"`
}

var profiles = map[string]AgentProfile{
	"blix_code_v1":         {ID: "blix_code_v1", Version: 1, RuntimeKind: "api", Tools: []string{}, Policy: model.ExecutionPolicy{Queue: "background", DeadlineSeconds: 3600, MaxAttempts: 1}},
	"file_ingest_v1":       {ID: "file_ingest_v1", Version: 1, RuntimeKind: "ingestion", Tools: []string{}, Policy: model.ExecutionPolicy{Queue: "background", DeadlineSeconds: 14400, MaxAttempts: 3}},
	"document_qa_v1":       {ID: "document_qa_v1", Version: 1, RuntimeKind: "api", Tools: []string{"knowledge_document_search_v1"}, Policy: model.ExecutionPolicy{Queue: "interactive", ModelProfile: "document_qa", DeadlineSeconds: 300, MaxAttempts: 3}},
	"document_ingest_v1":   {ID: "document_ingest_v1", Version: 1, RuntimeKind: "api", Tools: []string{}, Policy: model.ExecutionPolicy{Queue: "background", ModelProfile: "curator", DeadlineSeconds: 14400, MaxAttempts: 3}},
	"blix_v1":              {ID: "blix_v1", Version: 1, RuntimeKind: "api", Tools: []string{"chart_render_v1", "knowledge_document_search_v1", "current_time_v1", "agent_execution_v1", "discord_read_v1", "discord_search_v1", "discord_webhook_send_v1", "knowledge_remember_v1", "knowledge_read_v1", "sentinel_api_v1", "sentinel_identity_resolve_v1", "sentinel_impersonate_v1", "web_extract_v1", "web_search_v1", "workbench_catalog_v1", "workbench_query_v1"}, Executors: []string{"deep_research_v1", "discord_research_v1", "knowledge_curator_v1", "blix_code_v1"}, Policy: model.ExecutionPolicy{Queue: "interactive", ModelProfile: "lead", DeadlineSeconds: 3600, MaxAttempts: 3}},
	"deep_research_v1":     {ID: "deep_research_v1", Version: 1, RuntimeKind: "api", Tools: []string{"agent_ask_v1", "current_time_v1", "web_search_v1", "web_extract_v1"}, Policy: model.ExecutionPolicy{Queue: "background", ModelProfile: "research", DeadlineSeconds: 1800, MaxAttempts: 3}},
	"discord_research_v1":  {ID: "discord_research_v1", Version: 1, RuntimeKind: "api", Tools: []string{"agent_ask_v1", "current_time_v1", "discord_read_v1", "discord_search_v1"}, Policy: model.ExecutionPolicy{Queue: "background", ModelProfile: "research", DeadlineSeconds: 1800, MaxAttempts: 3}},
	"knowledge_curator_v1": {ID: "knowledge_curator_v1", Version: 1, RuntimeKind: "api", Tools: []string{"agent_ask_v1", "knowledge_read_v1", "knowledge_evidence_read_v1", "knowledge_source_create_v1", "knowledge_source_record_create_v1", "knowledge_propose_v1", "knowledge_apply_v1", "knowledge_document_read_v1"}, Policy: model.ExecutionPolicy{Queue: "background", ModelProfile: "curator", DeadlineSeconds: 1800, MaxAttempts: 3}},
}
