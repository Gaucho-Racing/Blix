package service

import "sync"

const workerSubscriberBuffer = 64

type WorkerHub struct {
	mutex         sync.Mutex
	nextID        uint64
	subscriptions map[uint64]chan WorkerSummary
}

func NewWorkerHub() *WorkerHub {
	return &WorkerHub{subscriptions: make(map[uint64]chan WorkerSummary)}
}

func (h *WorkerHub) Subscribe() (<-chan WorkerSummary, func()) {
	h.mutex.Lock()
	h.nextID++
	id := h.nextID
	updates := make(chan WorkerSummary, workerSubscriberBuffer)
	h.subscriptions[id] = updates
	h.mutex.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mutex.Lock()
			if current, exists := h.subscriptions[id]; exists {
				delete(h.subscriptions, id)
				close(current)
			}
			h.mutex.Unlock()
		})
	}
	return updates, cancel
}

func (h *WorkerHub) Publish(worker WorkerSummary) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	for id, updates := range h.subscriptions {
		select {
		case updates <- worker:
		default:
			delete(h.subscriptions, id)
			close(updates)
		}
	}
}
