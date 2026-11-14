package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

//go:embed pricing.json
var embedded []byte

const nanosPerUSD = 1e9

type TokenRates struct {
	Input       float64  `json:"input"`
	CachedInput *float64 `json:"cached_input,omitempty"`
	Output      float64  `json:"output"`
}

type LongContextRates struct {
	TokenRates
	AboveInputTokens uint64 `json:"above_input_tokens"`
}

type ModelPrice struct {
	TokenRates
	LongContext *LongContextRates `json:"long_context,omitempty"`
}

type ToolPrice struct {
	UnitPrice    float64           `json:"unit_price"`
	Units        map[string]uint64 `json:"units"`
	PerSuccesses uint64            `json:"per_successes,omitempty"`
}

type Table struct {
	Version  string                `json:"version"`
	Currency string                `json:"currency"`
	Sources  []string              `json:"sources"`
	Models   map[string]ModelPrice `json:"models"`
	Aliases  map[string]string     `json:"aliases"`
	Tools    map[string]ToolPrice  `json:"tools"`
}

type TokenUsage struct {
	InputTokens       uint64
	CachedInputTokens uint64
	OutputTokens      uint64
}

func Load() (*Table, error) {
	var table Table
	if err := json.Unmarshal(embedded, &table); err != nil {
		return nil, fmt.Errorf("parse pricing table: %w", err)
	}
	if err := table.validate(); err != nil {
		return nil, err
	}
	return &table, nil
}

func (t *Table) validate() error {
	if t.Version == "" || t.Currency != "USD" {
		return fmt.Errorf("pricing table needs a version and USD currency")
	}
	for name, price := range t.Models {
		if err := price.TokenRates.validate(); err != nil {
			return fmt.Errorf("pricing for model %q: %w", name, err)
		}
		if price.LongContext != nil {
			if price.LongContext.AboveInputTokens == 0 {
				return fmt.Errorf("pricing for model %q: long_context needs above_input_tokens", name)
			}
			if err := price.LongContext.TokenRates.validate(); err != nil {
				return fmt.Errorf("pricing for model %q long context: %w", name, err)
			}
		}
	}
	for alias, target := range t.Aliases {
		if _, ok := t.Models[target]; !ok {
			return fmt.Errorf("pricing alias %q points at unknown model %q", alias, target)
		}
	}
	for name, tool := range t.Tools {
		if tool.UnitPrice < 0 || len(tool.Units) == 0 {
			return fmt.Errorf("pricing for tool %q needs a unit_price and units", name)
		}
	}
	return nil
}

func (r TokenRates) validate() error {
	if r.Input < 0 || r.Output < 0 || (r.CachedInput != nil && *r.CachedInput < 0) {
		return fmt.Errorf("token rates must not be negative")
	}
	return nil
}

func (t *Table) Model(name string) (ModelPrice, bool) {
	name = strings.TrimSpace(name)
	if price, ok := t.Models[name]; ok {
		return price, true
	}
	if target, ok := t.Aliases[name]; ok {
		price, ok := t.Models[target]
		return price, ok
	}
	return ModelPrice{}, false
}

func (t *Table) ModelCostNanos(name string, usage TokenUsage) (int64, bool) {
	price, ok := t.Model(name)
	if !ok {
		return 0, false
	}
	rates := price.TokenRates
	if price.LongContext != nil && usage.InputTokens > price.LongContext.AboveInputTokens {
		rates = price.LongContext.TokenRates
	}
	cached := min(usage.CachedInputTokens, usage.InputTokens)
	cachedRate := rates.Input
	if rates.CachedInput != nil {
		cachedRate = *rates.CachedInput
	}
	perMillion := float64(usage.InputTokens-cached)*rates.Input + float64(cached)*cachedRate + float64(usage.OutputTokens)*rates.Output
	return toNanos(perMillion / 1e6), true
}

func (t *Table) ToolUnits(toolID, depth string, successes uint64) (uint64, bool) {
	tool, ok := t.Tools[toolID]
	if !ok {
		return 0, false
	}
	units, ok := tool.Units[depth]
	if !ok {
		return 0, false
	}
	if tool.PerSuccesses == 0 {
		return units, true
	}
	return units * ((successes + tool.PerSuccesses - 1) / tool.PerSuccesses), true
}

func (t *Table) ToolCostNanos(toolID string, units uint64) int64 {
	return toNanos(float64(units) * t.Tools[toolID].UnitPrice)
}

func toNanos(usd float64) int64 {
	return int64(math.Round(usd * nanosPerUSD))
}

func NanosToUSD(nanos int64) float64 {
	return float64(nanos) / nanosPerUSD
}
