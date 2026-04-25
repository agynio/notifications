package stream

import (
	"sync"

	"go.uber.org/zap"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

type subscriber struct {
	id    int
	ch    chan *notificationsv1.NotificationEnvelope
	end   bool
	rooms map[string]struct{}
}

// Hub fan-outs envelopes to registered subscribers with bounded buffers.
type Hub struct {
	mu         sync.Mutex
	logger     *zap.Logger
	bufferSize int
	nextID     int
	subs       map[int]*subscriber
	roomSubs   map[string]map[int]*subscriber
}

// NewHub creates a hub for broadcasting notifications to consumers.
func NewHub(bufferSize int, logger *zap.Logger) *Hub {
	if bufferSize <= 0 {
		bufferSize = 1
	}
	return &Hub{
		logger:     logger,
		bufferSize: bufferSize,
		subs:       make(map[int]*subscriber),
		roomSubs:   make(map[string]map[int]*subscriber),
	}
}

// Broadcast delivers the envelope to subscribers of the envelope rooms. Slow
// subscribers are dropped and their channels are closed.
func (h *Hub) Broadcast(envelope *notificationsv1.NotificationEnvelope) {
	if envelope == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	delivered := make(map[int]struct{})
	for _, room := range envelope.GetRooms() {
		subscribers := h.roomSubs[room]
		for id, sub := range subscribers {
			if sub.end {
				continue
			}
			if _, ok := delivered[id]; ok {
				continue
			}
			select {
			case sub.ch <- envelope:
				delivered[id] = struct{}{}
			default:
				h.dropSubscriberLocked(id, sub, "slow consumer")
			}
		}
	}
}

// Subscribe registers a new consumer and returns a read-only channel alongside
// a closure to remove the consumer.
func (h *Hub) Subscribe(rooms []string) (<-chan *notificationsv1.NotificationEnvelope, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.nextID
	h.nextID++

	ch := make(chan *notificationsv1.NotificationEnvelope, h.bufferSize)
	sub := &subscriber{id: id, ch: ch, rooms: make(map[string]struct{})}
	h.subs[id] = sub
	for _, room := range rooms {
		if _, ok := sub.rooms[room]; ok {
			continue
		}
		sub.rooms[room] = struct{}{}
		if h.roomSubs[room] == nil {
			h.roomSubs[room] = make(map[int]*subscriber)
		}
		h.roomSubs[room][id] = sub
	}

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if sub, ok := h.subs[id]; ok {
			h.dropSubscriberLocked(id, sub, "unsubscribed")
		}
	}
}

func (h *Hub) dropSubscriberLocked(id int, sub *subscriber, reason string) {
	if sub.end {
		return
	}
	sub.end = true
	close(sub.ch)
	for room := range sub.rooms {
		if subscribers, ok := h.roomSubs[room]; ok {
			delete(subscribers, id)
			if len(subscribers) == 0 {
				delete(h.roomSubs, room)
			}
		}
	}
	delete(h.subs, id)
	if h.logger != nil {
		h.logger.Warn("dropping subscriber", zap.Int("subscriber_id", id), zap.String("reason", reason))
	}
}
