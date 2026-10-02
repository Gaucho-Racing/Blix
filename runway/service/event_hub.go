package service

import (
	"sync"

	"github.com/gaucho-racing/blix/runway/model"
)

const eventSubscriberBuffer = 256

type eventSubscription struct {
	runID  string
	events chan model.Event
}

type EventHub struct {
	mutex         sync.Mutex
	nextID        uint64
	subscriptions map[uint64]eventSubscription
}

func NewEventHub() *EventHub {
	return &EventHub{subscriptions: make(map[uint64]eventSubscription)}
}

func (h *EventHub) Subscribe(runID string) (<-chan model.Event, func()) {
	h.mutex.Lock()
	h.nextID++
	id := h.nextID
	subscription := eventSubscription{
		runID:  runID,
		events: make(chan model.Event, eventSubscriberBuffer),
	}
	h.subscriptions[id] = subscription
	h.mutex.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mutex.Lock()
			if current, exists := h.subscriptions[id]; exists {
				delete(h.subscriptions, id)
				close(current.events)
			}
			h.mutex.Unlock()
		})
	}
	return subscription.events, cancel
}

func (h *EventHub) Publish(event model.Event) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	for id, subscription := range h.subscriptions {
		if subscription.runID != "" && subscription.runID != event.RunID {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			delete(h.subscriptions, id)
			close(subscription.events)
		}
	}
}
