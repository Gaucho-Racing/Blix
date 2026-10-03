package service

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/identifier"
)

func TestChannelInputAtomicReplayAndOwnership(t *testing.T) {
	f := setup(t)
	input := ChannelInput{Source: "discord", ExternalMessageID: "123456789", Content: "Explain the braking system", Context: json.RawMessage(`{"guild_id":"123","channel_id":"456","thread_id":"789"}`)}
	var wg sync.WaitGroup
	results := make(chan ChannelInputResult, 6)
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.s.SubmitChannelInput(f.ctx, f.owner, input)
			results <- r
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first ChannelInputResult
	for r := range results {
		if first.RunID == "" {
			first = r
		}
		if r != first {
			t.Fatalf("duplicate input produced different results: %+v %+v", first, r)
		}
	}
	var count int64
	if err := f.db.Model(&model.Message{}).Where("run_id = ?", first.RunID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("messages=%d err=%v", count, err)
	}
	var run model.Run
	if err := f.db.First(&run, "id = ?", first.RunID).Error; err != nil {
		t.Fatal(err)
	}
	var origin map[string]any
	if json.Unmarshal(run.Context, &origin) != nil || origin["source"] != "discord" || run.CallerEntityID != f.owner.EntityID {
		t.Fatalf("lost channel context or owner: %+v", run)
	}
	changed := input
	changed.Context = json.RawMessage(`{ "thread_id": "789", "guild_id": "123", "channel_id": "456" }`)
	if replay, err := f.s.SubmitChannelInput(f.ctx, f.owner, changed); err != nil || replay != first {
		t.Fatalf("reordered JSON changed replay: %+v %v", replay, err)
	}
	changed.Content = "Different input"
	if _, err := f.s.SubmitChannelInput(f.ctx, f.owner, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	other := model.CallerContext{EntityID: identifier.New(identifier.Entity), EntityType: "USER", UserID: identifier.New(identifier.User)}
	changed.ExternalMessageID = "987654321"
	changed.RunID = first.RunID
	if _, err := f.s.SubmitChannelInput(f.ctx, other, changed); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner input err=%v", err)
	}
	changed.RunID = f.run.Run.ID
	if _, err := f.s.SubmitChannelInput(f.ctx, f.owner, changed); !errors.Is(err, ErrForbidden) {
		t.Fatalf("regular chat accepted Discord input: %v", err)
	}
	changed.RunID = first.RunID
	follow, err := f.s.SubmitChannelInput(f.ctx, f.owner, changed)
	if err != nil || follow.RunID != first.RunID || follow.TurnID == first.TurnID {
		t.Fatalf("followup=%+v err=%v", follow, err)
	}
}

func TestChannelReplyPreservesCanonicalAnswer(t *testing.T) {
	f := setup(t)
	input := ChannelInput{Source: "discord", ExternalMessageID: "123456789", Content: "A question", Context: json.RawMessage(`{"guild_id":"123"}`)}
	r, err := f.s.SubmitChannelInput(f.ctx, f.owner, input)
	if err != nil {
		t.Fatal(err)
	}
	var attempt model.ExecutionAttempt
	if err = f.db.First(&attempt, "turn_id = ?", r.TurnID).Error; err != nil {
		t.Fatal(err)
	}
	a := f.claim(t, attempt.ID)
	if err = f.s.Complete(f.ctx, a.Attempt.ID, CompletionRequest{Ownership: own(a), Content: "Full answer with detailed evidence.", ChannelContent: "Short Discord answer.", ChannelHasSources: true}); err != nil {
		t.Fatal(err)
	}
	var message model.Message
	if err = f.db.First(&message, "run_id = ? AND role = 'assistant'", r.RunID).Error; err != nil {
		t.Fatal(err)
	}
	if message.Content != "Full answer with detailed evidence." || message.ChannelContent != "Short Discord answer." || !message.ChannelHasSources {
		t.Fatalf("lost reply variants: %+v", message)
	}
	var event model.Event
	if err = f.db.First(&event, "run_id = ? AND type = 'message.assistant_v1'", r.RunID).Error; err != nil {
		t.Fatal(err)
	}
	var payload struct {
		MessageID         string `json:"message_id"`
		ChannelHasSources bool   `json:"channel_has_sources"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.MessageID != message.ID || !payload.ChannelHasSources {
		t.Fatalf("lost source metadata: payload=%s error=%v", event.Payload, err)
	}
}
