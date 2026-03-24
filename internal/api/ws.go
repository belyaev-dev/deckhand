package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/gorilla/websocket"
)

const (
	defaultWSClientBuffer  = 8
	defaultWSRegisterQueue = 16
	defaultWSPongWait      = 60 * time.Second
	defaultWSPingPeriod    = 54 * time.Second
	defaultWSWriteWait     = 10 * time.Second
	defaultWSReadLimit     = 1024
)

// WSChangeEvent is the redacted browser-facing invalidation message emitted by /ws.
type WSChangeEvent struct {
	Type       string             `json:"type"`
	Kind       store.ResourceKind `json:"kind"`
	Action     store.Action       `json:"action"`
	Namespace  string             `json:"namespace"`
	Name       string             `json:"name"`
	OccurredAt time.Time          `json:"occurredAt"`
}

// WSHub multiplexes store change events to connected WebSocket clients.
type WSHub struct {
	logger *slog.Logger
	store  *store.Store

	upgrader websocket.Upgrader

	register   chan *wsClient
	unregister chan *wsClient

	clientBuffer int
	pongWait     time.Duration
	pingPeriod   time.Duration
	writeWait    time.Duration
	readLimit    int64

	clients map[*wsClient]struct{}

	readyOnce sync.Once
	readyCh   chan struct{}
}

type wsClient struct {
	hub        *WSHub
	conn       *websocket.Conn
	send       chan []byte
	remoteAddr string
}

// NewWSHub constructs a store-backed WebSocket hub with runtime defaults.
func NewWSHub(logger *slog.Logger, st *store.Store) *WSHub {
	if st == nil {
		st = store.New()
	}

	return &WSHub{
		logger: ensureLogger(logger),
		store:  st,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		register:     make(chan *wsClient, defaultWSRegisterQueue),
		unregister:   make(chan *wsClient, defaultWSRegisterQueue),
		clientBuffer: defaultWSClientBuffer,
		pongWait:     defaultWSPongWait,
		pingPeriod:   defaultWSPingPeriod,
		writeWait:    defaultWSWriteWait,
		readLimit:    defaultWSReadLimit,
		clients:      make(map[*wsClient]struct{}),
		readyCh:      make(chan struct{}),
	}
}

// Ready closes after the hub has subscribed to the store and is ready to serve clients.
func (h *WSHub) Ready() <-chan struct{} {
	if h == nil {
		ready := make(chan struct{})
		close(ready)
		return ready
	}
	return h.readyCh
}

// Start runs the hub event loop until ctx is canceled.
func (h *WSHub) Start(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("websocket hub is required")
	}
	if ctx == nil {
		return fmt.Errorf("websocket hub context is required")
	}

	storeEvents, unsubscribe := h.store.Subscribe(h.clientBuffer * 2)
	defer unsubscribe()

	h.logger.Info("starting websocket hub")
	h.readyOnce.Do(func() {
		close(h.readyCh)
	})

	for {
		select {
		case client := <-h.register:
			h.clients[client] = struct{}{}
			h.logger.Info("websocket client connected",
				"remote_addr", client.remoteAddr,
				"clients", len(h.clients),
			)
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.logger.Info("websocket client disconnected",
					"remote_addr", client.remoteAddr,
					"clients", len(h.clients),
				)
			}
		case event, ok := <-storeEvents:
			if !ok {
				h.logger.Info("websocket hub store subscription closed")
				h.closeClients()
				return nil
			}
			h.broadcast(event)
		case <-ctx.Done():
			h.closeClients()
			h.logger.Info("websocket hub stopped", "reason", ctx.Err())
			return nil
		}
	}
}

// ServeWS upgrades the HTTP request and registers the client with the hub.
func (h *WSHub) ServeWS(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("websocket upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		hub:        h,
		conn:       conn,
		send:       make(chan []byte, h.clientBuffer),
		remoteAddr: r.RemoteAddr,
	}

	h.register <- client
	go client.writePump()
	go client.readPump()
}

func (h *WSHub) broadcast(event store.ChangeEvent) {
	payload, err := json.Marshal(WSChangeEvent{
		Type:       "store.changed",
		Kind:       event.Kind,
		Action:     event.Action,
		Namespace:  event.Namespace,
		Name:       event.Name,
		OccurredAt: event.OccurredAt.UTC(),
	})
	if err != nil {
		h.logger.Error("failed to marshal websocket event", "error", err)
		return
	}

	for client := range h.clients {
		select {
		case client.send <- payload:
		default:
			delete(h.clients, client)
			close(client.send)
			h.logger.Warn("dropped slow websocket client",
				"remote_addr", client.remoteAddr,
				"clients", len(h.clients),
			)
		}
	}
}

func (h *WSHub) closeClients() {
	for client := range h.clients {
		delete(h.clients, client)
		close(client.send)
	}
}

func (c *wsClient) readPump() {
	defer func() {
		select {
		case c.hub.unregister <- c:
		default:
		}
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(c.hub.readLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.hub.pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(c.hub.pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *wsClient) writePump() {
	ticker := time.NewTicker(c.hub.pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

var _ wsHandler = (*WSHub)(nil)
