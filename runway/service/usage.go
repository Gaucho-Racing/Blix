package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
	"github.com/gaucho-racing/blix/runway/pricing"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxCostPeriodDays = 366
	topCostRows       = 25
)

type UsageService struct {
	database *gorm.DB
	prices   *pricing.Table
}

func NewUsageService(database *gorm.DB, prices *pricing.Table) *UsageService {
	return &UsageService{database: database, prices: prices}
}

type ExternalUsageRequest struct {
	SourceKey    string `json:"source_key"`
	Category     string `json:"category"`
	Resource     string `json:"resource"`
	ProfileID    string `json:"profile_id"`
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
}

type CostTotals struct {
	CostUSD       float64 `json:"cost_usd"`
	Calls         uint64  `json:"calls"`
	UnpricedCalls uint64  `json:"unpriced_calls"`
}

type DailyCost struct {
	Day      string  `json:"day"`
	Category string  `json:"category"`
	Resource string  `json:"resource"`
	CostUSD  float64 `json:"cost_usd"`
	Calls    uint64  `json:"calls"`
}

type ResourceCost struct {
	Category          string  `json:"category"`
	Resource          string  `json:"resource"`
	Calls             uint64  `json:"calls"`
	UnpricedCalls     uint64  `json:"unpriced_calls"`
	InputTokens       uint64  `json:"input_tokens"`
	CachedInputTokens uint64  `json:"cached_input_tokens"`
	OutputTokens      uint64  `json:"output_tokens"`
	ReasoningTokens   uint64  `json:"reasoning_tokens"`
	Units             uint64  `json:"units"`
	CostUSD           float64 `json:"cost_usd"`
}

type ProfileCost struct {
	ProfileID string  `json:"profile_id"`
	Calls     uint64  `json:"calls"`
	CostUSD   float64 `json:"cost_usd"`
}

type CallerCost struct {
	CallerEntityID string  `json:"caller_entity_id"`
	Runs           uint64  `json:"runs"`
	CostUSD        float64 `json:"cost_usd"`
}

type RunCost struct {
	RunID          string    `json:"run_id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	CallerEntityID string    `json:"caller_entity_id"`
	CreatedAt      time.Time `json:"created_at"`
	CostUSD        float64   `json:"cost_usd"`
}

type CostSummary struct {
	PricingVersion string         `json:"pricing_version"`
	Currency       string         `json:"currency"`
	Days           int            `json:"days"`
	Since          time.Time      `json:"since"`
	Period         CostTotals     `json:"period"`
	Today          CostTotals     `json:"today"`
	Last7Days      CostTotals     `json:"last_7_days"`
	Daily          []DailyCost    `json:"daily"`
	Resources      []ResourceCost `json:"resources"`
	Profiles       []ProfileCost  `json:"profiles"`
	Callers        []CallerCost   `json:"callers"`
	Runs           []RunCost      `json:"runs"`
}

type SessionCost struct {
	SessionID string  `json:"session_id"`
	ProfileID string  `json:"profile_id"`
	Calls     uint64  `json:"calls"`
	CostUSD   float64 `json:"cost_usd"`
}

type RunCostBreakdown struct {
	RunID          string         `json:"run_id"`
	PricingVersion string         `json:"pricing_version"`
	Currency       string         `json:"currency"`
	Total          CostTotals     `json:"total"`
	Resources      []ResourceCost `json:"resources"`
	Sessions       []SessionCost  `json:"sessions"`
}

type usageAttribution struct {
	CallerEntityID string
	ProfileID      string
}

func (s *UsageService) attribution(tx *gorm.DB, sessionID string) (usageAttribution, error) {
	var row usageAttribution
	err := tx.Raw(`SELECT run.caller_entity_id, session.profile_id
		FROM runway_agent_session session JOIN runway_run run ON run.id = session.run_id
		WHERE session.id = ?`, sessionID).Scan(&row).Error
	return row, err
}

func (s *UsageService) insert(tx *gorm.DB, record model.UsageRecord) (bool, error) {
	record.ID = identifier.New(identifier.Usage)
	record.PricingVersion = s.prices.Version
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_key"}}, DoNothing: true}).Create(&record)
	return result.RowsAffected == 1, result.Error
}

func (s *UsageService) recordModelResponse(tx *gorm.DB, a model.ExecutionAttempt, eventID string, payload json.RawMessage) error {
	return s.recordModelResponseAt(tx, a, eventID, payload, time.Now().UTC())
}

func (s *UsageService) recordModelResponseAt(tx *gorm.DB, a model.ExecutionAttempt, eventID string, payload json.RawMessage, at time.Time) error {
	var usage struct {
		Model             string `json:"model"`
		InputTokens       uint64 `json:"input_tokens"`
		CachedInputTokens uint64 `json:"cached_input_tokens"`
		OutputTokens      uint64 `json:"output_tokens"`
		ReasoningTokens   uint64 `json:"reasoning_tokens"`
	}
	if err := json.Unmarshal(payload, &usage); err != nil {
		return ErrInvalid
	}
	who, err := s.attribution(tx, a.SessionID)
	if err != nil {
		return err
	}
	cost, priced := s.prices.ModelCostNanos(usage.Model, pricing.TokenUsage{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
	})
	_, err = s.insert(tx, model.UsageRecord{
		SourceKey:         "event:" + eventID,
		Category:          model.UsageCategoryModel,
		Resource:          resourceName(usage.Model),
		RunID:             &a.RunID,
		SessionID:         &a.SessionID,
		WorkerID:          a.WorkerID,
		CallerEntityID:    who.CallerEntityID,
		ProfileID:         who.ProfileID,
		InputTokens:       usage.InputTokens,
		CachedInputTokens: min(usage.CachedInputTokens, usage.InputTokens),
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   min(usage.ReasoningTokens, usage.OutputTokens),
		CostNanos:         cost,
		Priced:            priced,
		CreatedAt:         at,
	})
	return err
}

func (s *UsageService) recordToolResult(tx *gorm.DB, a model.ExecutionAttempt, action model.Action, result json.RawMessage) error {
	if _, ok := s.prices.Tools[action.ToolID]; !ok {
		return nil
	}
	var outcome struct {
		Status string `json:"status"`
		Output struct {
			Results []json.RawMessage `json:"results"`
		} `json:"output"`
	}
	if err := json.Unmarshal(result, &outcome); err != nil || outcome.Status != "succeeded" {
		return nil
	}
	var arguments struct {
		SearchDepth  string `json:"search_depth"`
		ExtractDepth string `json:"extract_depth"`
	}
	_ = json.Unmarshal(action.Arguments, &arguments)
	depth := firstNonEmpty(arguments.SearchDepth, arguments.ExtractDepth, "basic")
	units, priced := s.prices.ToolUnits(action.ToolID, depth, uint64(len(outcome.Output.Results)))
	if units == 0 && priced {
		return nil
	}
	who, err := s.attribution(tx, a.SessionID)
	if err != nil {
		return err
	}
	cost := int64(0)
	if priced {
		cost = s.prices.ToolCostNanos(action.ToolID, units)
	}
	_, err = s.insert(tx, model.UsageRecord{
		SourceKey:      "action:" + action.ID,
		Category:       model.UsageCategoryTool,
		Resource:       action.ToolID,
		RunID:          &a.RunID,
		SessionID:      &a.SessionID,
		WorkerID:       a.WorkerID,
		CallerEntityID: who.CallerEntityID,
		ProfileID:      who.ProfileID,
		Units:          units,
		CostNanos:      cost,
		Priced:         priced,
	})
	return err
}

func (s *UsageService) RecordExternal(ctx context.Context, request ExternalUsageRequest) (bool, error) {
	request.SourceKey = strings.TrimSpace(request.SourceKey)
	request.Resource = strings.TrimSpace(request.Resource)
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	if request.Category != model.UsageCategoryEmbedding || request.SourceKey == "" || len(request.SourceKey) > 200 ||
		request.Resource == "" || len(request.Resource) > 200 || len(request.ProfileID) > 100 ||
		request.InputTokens == 0 || request.InputTokens > 1<<32 || request.OutputTokens > 1<<32 {
		return false, ErrInvalid
	}
	cost, priced := s.prices.ModelCostNanos(request.Resource, pricing.TokenUsage{InputTokens: request.InputTokens, OutputTokens: request.OutputTokens})
	return s.insert(s.database.WithContext(ctx), model.UsageRecord{
		SourceKey:    "external:" + request.SourceKey,
		Category:     request.Category,
		Resource:     request.Resource,
		ProfileID:    request.ProfileID,
		InputTokens:  request.InputTokens,
		OutputTokens: request.OutputTokens,
		CostNanos:    cost,
		Priced:       priced,
	})
}

const resourceColumns = `category, resource,
	COUNT(*) AS calls,
	COUNT(*) FILTER (WHERE NOT priced) AS unpriced_calls,
	COALESCE(SUM(input_tokens),0) AS input_tokens,
	COALESCE(SUM(cached_input_tokens),0) AS cached_input_tokens,
	COALESCE(SUM(output_tokens),0) AS output_tokens,
	COALESCE(SUM(reasoning_tokens),0) AS reasoning_tokens,
	COALESCE(SUM(units),0) AS units,
	COALESCE(SUM(cost_nanos),0) AS cost_nanos`

type resourceCostRow struct {
	ResourceCost
	CostNanos int64
}

type totalsRow struct {
	CostNanos     int64
	Calls         uint64
	UnpricedCalls uint64
}

func (r totalsRow) totals() CostTotals {
	return CostTotals{CostUSD: pricing.NanosToUSD(r.CostNanos), Calls: r.Calls, UnpricedCalls: r.UnpricedCalls}
}

func (s *UsageService) Summary(ctx context.Context, days int, category string) (CostSummary, error) {
	if days < 1 || days > maxCostPeriodDays || (category != "" && category != model.UsageCategoryModel && category != model.UsageCategoryTool && category != model.UsageCategoryEmbedding) {
		return CostSummary{}, ErrInvalid
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	since := today.AddDate(0, 0, -(days - 1))
	db := s.database.WithContext(ctx)
	scoped := func() *gorm.DB {
		q := db.Table("runway_usage_record")
		if category != "" {
			q = q.Where("category = ?", category)
		}
		return q
	}
	summary := CostSummary{PricingVersion: s.prices.Version, Currency: s.prices.Currency, Days: days, Since: since}

	totalsSelect := "COALESCE(SUM(cost_nanos),0) AS cost_nanos, COUNT(*) AS calls, COUNT(*) FILTER (WHERE NOT priced) AS unpriced_calls"
	for _, window := range []struct {
		from time.Time
		dest *CostTotals
	}{{since, &summary.Period}, {today, &summary.Today}, {today.AddDate(0, 0, -6), &summary.Last7Days}} {
		var row totalsRow
		if err := scoped().Select(totalsSelect).Where("created_at >= ?", window.from).Scan(&row).Error; err != nil {
			return CostSummary{}, err
		}
		*window.dest = row.totals()
	}

	var daily []struct {
		Day       time.Time
		Category  string
		Resource  string
		CostNanos int64
		Calls     uint64
	}
	if err := scoped().
		Select("date_trunc('day', created_at AT TIME ZONE 'UTC') AS day, category, resource, SUM(cost_nanos) AS cost_nanos, COUNT(*) AS calls").
		Where("created_at >= ?", since).Group("day, category, resource").Order("day, category, resource").
		Scan(&daily).Error; err != nil {
		return CostSummary{}, err
	}
	summary.Daily = make([]DailyCost, 0, len(daily))
	for _, row := range daily {
		summary.Daily = append(summary.Daily, DailyCost{Day: row.Day.Format(time.DateOnly), Category: row.Category, Resource: row.Resource, CostUSD: pricing.NanosToUSD(row.CostNanos), Calls: row.Calls})
	}

	var resources []resourceCostRow
	if err := scoped().Select(resourceColumns).Where("created_at >= ?", since).
		Group("category, resource").Order("cost_nanos DESC, calls DESC").Scan(&resources).Error; err != nil {
		return CostSummary{}, err
	}
	summary.Resources = resourceCosts(resources)

	var profiles []struct {
		ProfileID string
		Calls     uint64
		CostNanos int64
	}
	if err := scoped().Select("profile_id, COUNT(*) AS calls, SUM(cost_nanos) AS cost_nanos").Where("created_at >= ?", since).
		Group("profile_id").Order("cost_nanos DESC").Limit(topCostRows).Scan(&profiles).Error; err != nil {
		return CostSummary{}, err
	}
	summary.Profiles = make([]ProfileCost, 0, len(profiles))
	for _, row := range profiles {
		summary.Profiles = append(summary.Profiles, ProfileCost{ProfileID: row.ProfileID, Calls: row.Calls, CostUSD: pricing.NanosToUSD(row.CostNanos)})
	}

	var callers []struct {
		CallerEntityID string
		Runs           uint64
		CostNanos      int64
	}
	if err := scoped().Select("caller_entity_id, COUNT(DISTINCT run_id) AS runs, SUM(cost_nanos) AS cost_nanos").
		Where("created_at >= ? AND caller_entity_id <> ''", since).
		Group("caller_entity_id").Order("cost_nanos DESC").Limit(topCostRows).Scan(&callers).Error; err != nil {
		return CostSummary{}, err
	}
	summary.Callers = make([]CallerCost, 0, len(callers))
	for _, row := range callers {
		summary.Callers = append(summary.Callers, CallerCost{CallerEntityID: row.CallerEntityID, Runs: row.Runs, CostUSD: pricing.NanosToUSD(row.CostNanos)})
	}

	var runs []struct {
		RunID          string
		Name           string
		Kind           string
		CallerEntityID string
		CreatedAt      time.Time
		CostNanos      int64
	}
	runQuery := db.Table("runway_usage_record AS usage").
		Select("run.id AS run_id, run.name, run.kind, run.caller_entity_id, run.created_at, SUM(usage.cost_nanos) AS cost_nanos").
		Joins("JOIN runway_run run ON run.id = usage.run_id").
		Where("usage.created_at >= ?", since)
	if category != "" {
		runQuery = runQuery.Where("usage.category = ?", category)
	}
	if err := runQuery.Group("run.id").Order("cost_nanos DESC").Limit(topCostRows).Scan(&runs).Error; err != nil {
		return CostSummary{}, err
	}
	summary.Runs = make([]RunCost, 0, len(runs))
	for _, row := range runs {
		summary.Runs = append(summary.Runs, RunCost{RunID: row.RunID, Name: row.Name, Kind: row.Kind, CallerEntityID: row.CallerEntityID, CreatedAt: row.CreatedAt, CostUSD: pricing.NanosToUSD(row.CostNanos)})
	}
	return summary, nil
}

func (s *UsageService) RunCosts(ctx context.Context, runIDs []string) (map[string]float64, error) {
	costs := make(map[string]float64, len(runIDs))
	if len(runIDs) == 0 {
		return costs, nil
	}
	var rows []struct {
		RunID     string
		CostNanos int64
	}
	if err := s.database.WithContext(ctx).Table("runway_usage_record").
		Select("run_id, SUM(cost_nanos) AS cost_nanos").Where("run_id IN ?", runIDs).Group("run_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		costs[row.RunID] = pricing.NanosToUSD(row.CostNanos)
	}
	return costs, nil
}

func (s *UsageService) RunBreakdown(ctx context.Context, runID string) (RunCostBreakdown, error) {
	if !identifier.IsValid(runID, identifier.Run) {
		return RunCostBreakdown{}, fmt.Errorf("%w: run_id is invalid", ErrInvalid)
	}
	db := s.database.WithContext(ctx)
	breakdown := RunCostBreakdown{RunID: runID, PricingVersion: s.prices.Version, Currency: s.prices.Currency}
	var total totalsRow
	if err := db.Table("runway_usage_record").
		Select("COALESCE(SUM(cost_nanos),0) AS cost_nanos, COUNT(*) AS calls, COUNT(*) FILTER (WHERE NOT priced) AS unpriced_calls").
		Where("run_id = ?", runID).Scan(&total).Error; err != nil {
		return RunCostBreakdown{}, err
	}
	breakdown.Total = total.totals()
	var resources []resourceCostRow
	if err := db.Table("runway_usage_record").Select(resourceColumns).Where("run_id = ?", runID).
		Group("category, resource").Order("cost_nanos DESC, calls DESC").Scan(&resources).Error; err != nil {
		return RunCostBreakdown{}, err
	}
	breakdown.Resources = resourceCosts(resources)
	var sessions []struct {
		SessionID string
		ProfileID string
		Calls     uint64
		CostNanos int64
	}
	if err := db.Table("runway_usage_record").Select("session_id, profile_id, COUNT(*) AS calls, SUM(cost_nanos) AS cost_nanos").
		Where("run_id = ?", runID).Group("session_id, profile_id").Order("cost_nanos DESC").Scan(&sessions).Error; err != nil {
		return RunCostBreakdown{}, err
	}
	breakdown.Sessions = make([]SessionCost, 0, len(sessions))
	for _, row := range sessions {
		breakdown.Sessions = append(breakdown.Sessions, SessionCost{SessionID: row.SessionID, ProfileID: row.ProfileID, Calls: row.Calls, CostUSD: pricing.NanosToUSD(row.CostNanos)})
	}
	return breakdown, nil
}

func resourceCosts(rows []resourceCostRow) []ResourceCost {
	costs := make([]ResourceCost, 0, len(rows))
	for _, row := range rows {
		cost := row.ResourceCost
		cost.CostUSD = pricing.NanosToUSD(row.CostNanos)
		costs = append(costs, cost)
	}
	return costs
}

func resourceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unknown"
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const backfillBatchSize = 500

func (s *UsageService) Backfill(ctx context.Context) (int, error) {
	total := 0
	for {
		fetched, inserted, err := s.backfillBatch(ctx)
		total += inserted
		if err != nil || fetched < backfillBatchSize || inserted == 0 {
			return total, err
		}
	}
}

func (s *UsageService) backfillBatch(ctx context.Context) (int, int, error) {
	var events []struct {
		ID        string
		RunID     string
		SessionID string
		WorkerID  string
		Payload   []byte
		CreatedAt time.Time
	}
	err := s.database.WithContext(ctx).Raw(`SELECT event.id, event.run_id, event.session_id, COALESCE(attempt.worker_id, '') AS worker_id, event.payload, event.created_at
		FROM runway_event event
		LEFT JOIN runway_execution_attempt attempt ON attempt.id = event.attempt_id
		WHERE event.type = 'model.response_v1'
			AND NOT EXISTS (SELECT 1 FROM runway_usage_record usage WHERE usage.source_key = 'event:' || event.id)
		ORDER BY event.position LIMIT ?`, backfillBatchSize).Scan(&events).Error
	if err != nil || len(events) == 0 {
		return 0, 0, err
	}
	inserted := 0
	err = s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, event := range events {
			attempt := model.ExecutionAttempt{RunID: event.RunID, SessionID: event.SessionID, WorkerID: event.WorkerID, CreatedAt: event.CreatedAt}
			err := s.recordModelResponseAt(tx, attempt, event.ID, event.Payload, event.CreatedAt)
			if errors.Is(err, ErrInvalid) {
				continue
			}
			if err != nil {
				return err
			}
			inserted++
		}
		return nil
	})
	return len(events), inserted, err
}
