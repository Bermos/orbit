// Package realtime fans domain events out to connected WebSocket clients.
//
// The hub is in-process for the prototype. Running several replicas needs a
// shared bus (NATS) behind Publish; the topic model stays the same.
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Event is what clients receive.
type Event struct {
	Topic string `json:"topic"`
	Type  string `json:"type"`
	Data  any    `json:"data"`
}

type client struct {
	send   chan []byte
	mu     sync.Mutex
	topics map[string]bool
}

func (c *client) subscribed(topic string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topics[topic]
}

// Hub tracks clients and their topic subscriptions.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: map[*client]struct{}{}}
}

// Publish sends an event to every client subscribed to its topic. Slow
// clients drop events instead of blocking the publisher.
func (h *Hub) Publish(topic, typ string, data any) {
	payload, err := json.Marshal(Event{Topic: topic, Type: typ, Data: data})
	if err != nil {
		slog.Error("marshal event", "err", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if !c.subscribed(topic) {
			continue
		}
		select {
		case c.send <- payload:
		default:
		}
	}
}

// clientMessage is what clients send: subscribe or unsubscribe to topics.
type clientMessage struct {
	Action string   `json:"action"`
	Topics []string `json:"topics"`
}

// Serve upgrades the request and runs the connection until it closes.
// authorize decides whether this connection may subscribe to a topic.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, authorize func(ctx context.Context, topic string) bool) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	c := &client{send: make(chan []byte, 64), topics: map[string]bool{}}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		for {
			var msg clientMessage
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			for _, t := range msg.Topics {
				switch msg.Action {
				case "subscribe":
					if authorize(ctx, t) {
						c.mu.Lock()
						c.topics[t] = true
						c.mu.Unlock()
					}
				case "unsubscribe":
					c.mu.Lock()
					delete(c.topics, t)
					c.mu.Unlock()
				}
			}
		}
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case msg := <-c.send:
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}
