package chat

import (
	"log/slog"
	"sync"
)

// subscriberBufferSize is the per-subscriber event buffer. A slow SSE client
// can absorb a burst (room activity, back-to-back messages) before the hub
// has to drop. On drop the client is expected to reconnect with ?since_seq=
// so the SSE handler replays missed message events from chat_messages.
const subscriberBufferSize = 128

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}}
}

func (h *Hub) Subscribe(userID string) (<-chan Event, func()) {
	ch := make(chan Event, subscriberBufferSize)
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

// Publish fans ev out to every subscriber of the listed users. A full
// subscriber buffer is not fatal: the frame is dropped for that subscriber
// and a warning logged so an operator can see the client is falling behind.
// The dropped client recovers missed message events via SSE replay on
// reconnect (?since_seq=).
func (h *Hub) Publish(userIDs []string, ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, uid := range userIDs {
		for ch := range h.subs[uid] {
			select {
			case ch <- ev:
			default:
				slog.Warn("chat: subscriber buffer full; dropping event",
					"user_id", uid, "event_type", ev.Type, "room_id", ev.RoomID)
			}
		}
	}
}
