package chat

import "sync"

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}}
}

func (h *Hub) Subscribe(userID string) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = map[chan Event]struct{}{}
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if set := h.subs[userID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, userID)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

func (h *Hub) Publish(userIDs []string, ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, uid := range userIDs {
		for ch := range h.subs[uid] {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}
