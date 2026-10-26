package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Limits struct {
	Connections int
	Frames      int
	Heartbeat   time.Duration
	Idle        time.Duration
	Lease       time.Duration
}

func DefaultLimits() Limits {
	return Limits{Connections: 3, Frames: 30, Heartbeat: 20 * time.Second, Idle: 75 * time.Second, Lease: 60 * time.Second}
}

type Gateway struct {
	Auth        *auth.HTTP
	Chat        *chat.Store
	Broker      *Broker
	Limits      Limits
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	connections map[*websocket.Conn]context.CancelFunc
	wg          sync.WaitGroup
}

func New(ctx context.Context, identity *auth.HTTP, store *chat.Store, broker *Broker) *Gateway {
	ctx, cancel := context.WithCancel(ctx)
	return &Gateway{Auth: identity, Chat: store, Broker: broker, Limits: DefaultLimits(), ctx: ctx, cancel: cancel, connections: map[*websocket.Conn]context.CancelFunc{}}
}
func (g *Gateway) Shutdown() {
	g.mu.Lock()
	g.closed = true
	g.cancel()
	for conn, cancel := range g.connections {
		cancel()
		_ = conn.CloseNow()
	}
	g.mu.Unlock()
	g.wg.Wait()
}
func (g *Gateway) check(ctx context.Context, session auth.Session) error {
	return g.Auth.Service.InTenant(ctx, session, policy.RealtimeConnect, "", func(database.TenantTx, policy.Actor) error { return nil })
}
func (g *Gateway) GetChannelPresence(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if g == nil {
		auth.JSON(w, 503, map[string]string{"error": "realtime unavailable"})
		return
	}
	session, ok := g.Auth.Require(w, r, policy.PresenceRead)
	if !ok {
		return
	}
	if id == uuid.Nil {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	// Authorize before touching Redis, then re-authorize the returned member projection.
	if err := g.Chat.WithChannel(r.Context(), session, id, policy.PresenceRead, func(database.TenantTx, policy.Actor) error { return nil }); err != nil {
		auth.Error(w, err)
		return
	}
	online, typing, err := g.Broker.Snapshot(r.Context(), session.WorkspaceID, id)
	if err != nil {
		auth.Error(w, err)
		return
	}
	members, err := g.Chat.PresenceMembers(r.Context(), session, id, online, typing)
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, members)
}

type frame struct {
	Type      string     `json:"type"`
	ChannelID *uuid.UUID `json:"channel_id,omitempty"`
	Active    *bool      `json:"active,omitempty"`
}

func decode(data []byte) (frame, error) {
	var f frame
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return f, auth.ErrInvalid
	}
	if err := decoder.Decode(new(interface{})); !errors.Is(err, io.EOF) {
		return f, auth.ErrInvalid
	}
	switch f.Type {
	case "heartbeat":
		if f.ChannelID != nil || f.Active != nil {
			return f, auth.ErrInvalid
		}
	case "subscribe", "unsubscribe":
		if f.ChannelID == nil || *f.ChannelID == uuid.Nil || f.Active != nil {
			return f, auth.ErrInvalid
		}
	case "typing":
		if f.ChannelID == nil || *f.ChannelID == uuid.Nil || f.Active == nil {
			return f, auth.ErrInvalid
		}
	default:
		return f, auth.ErrForbidden
	}
	return f, nil
}

type subscription struct {
	pubsub *redis.PubSub
	cancel context.CancelFunc
}
type connection struct {
	gateway       *Gateway
	ws            *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	session       auth.Session
	id            uuid.UUID
	mu            sync.Mutex
	subscriptions map[uuid.UUID]subscription
	directory     *subscription
	workers       sync.WaitGroup
	write         sync.Mutex
}

func (c *connection) send(value Notice) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.write.Lock()
	defer c.write.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, data)
}
func (c *connection) reject(code string) {
	_ = c.send(Notice{Type: "error", Code: code})
	_ = c.ws.Close(websocket.StatusPolicyViolation, code)
	c.cancel()
}
func (g *Gateway) GetRealtime(w http.ResponseWriter, r *http.Request) {
	if g == nil {
		auth.JSON(w, 503, map[string]string{"error": "realtime unavailable"})
		return
	}
	// Do not rely on websocket.Accept's implicit same-host exception or wildcards.
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != g.Auth.Origin {
		auth.Error(w, auth.ErrForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	session, ok := g.Auth.Require(w, r, policy.RealtimeConnect)
	if !ok {
		return
	}
	if err := g.check(r.Context(), session); err != nil {
		auth.Error(w, err)
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		auth.Error(w, err)
		return
	}
	allowed, err := g.Broker.Reserve(r.Context(), session.WorkspaceID, session.User.ID, id, g.Limits.Connections, g.Limits.Lease)
	if err != nil {
		auth.JSON(w, 503, map[string]string{"error": "realtime unavailable"})
		return
	}
	if !allowed {
		auth.Error(w, auth.ErrLimited)
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = g.Broker.Release(ctx, session.WorkspaceID, session.User.ID, id)
	}()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()
	origin, _ := url.Parse(g.Auth.Origin)
	// Control frames bypass Read's application messages, so cap them independently.
	var controlMu sync.Mutex
	window := time.Now()
	controlCount := 0
	controlAllowed := func() bool {
		controlMu.Lock()
		defer controlMu.Unlock()
		if time.Since(window) >= 10*time.Second {
			window = time.Now()
			controlCount = 0
		}
		controlCount++
		if controlCount > 64 {
			cancel()
			return false
		}
		return true
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{origin.Host}, CompressionMode: websocket.CompressionDisabled, OnPingReceived: func(context.Context, []byte) bool { return controlAllowed() }, OnPongReceived: func(context.Context, []byte) { controlAllowed() }})
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(4096)
	g.mu.Lock()
	if g.closed || g.ctx.Err() != nil {
		g.mu.Unlock()
		return
	}
	g.connections[ws] = cancel
	g.wg.Add(1)
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.connections, ws); g.mu.Unlock(); g.wg.Done() }()
	c := &connection{gateway: g, ws: ws, ctx: ctx, cancel: cancel, session: session, id: id, subscriptions: map[uuid.UUID]subscription{}}
	defer c.cleanup()
	if err := c.watch(uuid.Nil); err != nil {
		return
	}
	if err := c.send(Notice{Type: "ready"}); err != nil {
		return
	}
	c.workers.Add(1)
	go func() { defer c.workers.Done(); c.heartbeat() }()
	for {
		read, cancelRead := context.WithTimeout(ctx, g.Limits.Idle)
		kind, data, err := ws.Read(read)
		cancelRead()
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			_ = ws.Close(websocket.StatusUnsupportedData, "text frames required")
			return
		}
		allowed, err := g.Broker.FrameAllowed(ctx, session.WorkspaceID, session.User.ID, g.Limits.Frames)
		if err != nil {
			c.reject("unavailable")
			return
		}
		if !allowed {
			c.reject("rate_limited")
			return
		}
		f, err := decode(data)
		if err != nil {
			_ = policy.Can(ctx, session.Actor(), policy.Action("unknown"), policy.Resource{WorkspaceID: session.WorkspaceID.String()})
			c.reject("invalid_frame")
			return
		}
		if err := c.handle(f); err != nil {
			code := "not_permitted"
			if errors.Is(err, auth.ErrNotFound) {
				code = "not_found"
			}
			c.reject(code)
			return
		}
	}
}
func (c *connection) handle(f frame) error {
	g := c.gateway
	if f.Type == "heartbeat" {
		if err := g.check(c.ctx, c.session); err != nil {
			return err
		}
		return c.send(Notice{Type: "heartbeat"})
	}
	action := policy.RealtimeSubscribe
	if f.Type == "typing" {
		action = policy.TypingWrite
	}
	id := *f.ChannelID
	if err := g.Chat.WithChannel(c.ctx, c.session, id, action, func(database.TenantTx, policy.Actor) error { return nil }); err != nil {
		return err
	}
	switch f.Type {
	case "subscribe":
		c.mu.Lock()
		_, exists := c.subscriptions[id]
		count := len(c.subscriptions)
		c.mu.Unlock()
		if !exists {
			if count >= 8 {
				return auth.ErrLimited
			}
			if err := c.watch(id); err != nil {
				return err
			}
		}
		if err := g.Broker.Online(c.ctx, c.session.WorkspaceID, id, c.session.User.ID, c.id, g.Limits.Lease, true); err != nil {
			return err
		}
		if err := g.Broker.NotifyPresence(c.ctx, c.session.WorkspaceID, id); err != nil {
			return err
		}
		return c.send(Notice{Type: "subscribed", ChannelID: id.String()})
	case "unsubscribe":
		c.unwatch(id)
		return c.send(Notice{Type: "unsubscribed", ChannelID: id.String()})
	case "typing":
		c.mu.Lock()
		_, exists := c.subscriptions[id]
		c.mu.Unlock()
		if !exists {
			return auth.ErrForbidden
		}
		if err := g.Broker.Typing(c.ctx, c.session.WorkspaceID, id, c.session.User.ID, c.id, *f.Active); err != nil {
			return err
		}
		return g.Broker.NotifyPresence(c.ctx, c.session.WorkspaceID, id)
	}
	return auth.ErrForbidden
}
func (c *connection) watch(id uuid.UUID) error {
	key := directory(c.session.WorkspaceID)
	if id != uuid.Nil {
		key = topic(c.session.WorkspaceID, id)
	}
	ctx, cancel := context.WithCancel(c.ctx)
	sub := c.gateway.Broker.Redis.Subscribe(ctx, key)
	confirm, stop := context.WithTimeout(ctx, 3*time.Second)
	_, err := sub.Receive(confirm)
	stop()
	if err != nil {
		cancel()
		_ = sub.Close()
		return err
	}
	if id != uuid.Nil {
		c.mu.Lock()
		c.subscriptions[id] = subscription{sub, cancel}
		c.mu.Unlock()
	} else {
		c.directory = &subscription{sub, cancel}
	}
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer cancel()
		defer func() { _ = sub.Close() }()
		for {
			message, err := sub.ReceiveMessage(ctx)
			if err != nil {
				if ctx.Err() == nil {
					c.cancel()
				}
				return
			}
			if len(message.Payload) > 2048 {
				continue
			}
			var notice Notice
			if json.Unmarshal([]byte(message.Payload), &notice) != nil {
				continue
			}
			target, err := uuid.Parse(notice.ChannelID)
			if err != nil || target == uuid.Nil || (id != uuid.Nil && target != id) {
				continue
			}
			if notice.Type != "presence" && notice.Type != "event" {
				continue
			}
			if id == uuid.Nil && (notice.Type != "event" || notice.EventType != "channel.created") {
				continue
			}
			if notice.Type == "event" {
				if _, err := uuid.Parse(notice.EventID); err != nil {
					continue
				}
				if notice.EventType != "channel.created" && notice.EventType != "message.created" && notice.EventType != "message.updated" && notice.EventType != "message.deleted" {
					continue
				}
			}
			if err := c.gateway.Chat.WithChannel(ctx, c.session, target, policy.RealtimeSubscribe, func(database.TenantTx, policy.Actor) error { return nil }); err != nil {
				c.cancel()
				return
			}
			if err := c.send(notice); err != nil {
				c.cancel()
				return
			}
		}
	}()
	return nil
}
func (c *connection) unwatch(id uuid.UUID) {
	c.mu.Lock()
	sub, exists := c.subscriptions[id]
	delete(c.subscriptions, id)
	c.mu.Unlock()
	if exists {
		sub.cancel()
		_ = sub.pubsub.Close()
		c.leave(id)
	}
}
func (c *connection) leave(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b := c.gateway.Broker
	_ = b.Online(ctx, c.session.WorkspaceID, id, c.session.User.ID, c.id, c.gateway.Limits.Lease, false)
	_ = b.Typing(ctx, c.session.WorkspaceID, id, c.session.User.ID, c.id, false)
	_ = b.NotifyPresence(ctx, c.session.WorkspaceID, id)
}
func (c *connection) channels() []uuid.UUID {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]uuid.UUID, 0, len(c.subscriptions))
	for id := range c.subscriptions {
		ids = append(ids, id)
	}
	return ids
}
func (c *connection) cleanup() {
	c.cancel()
	if c.directory != nil {
		c.directory.cancel()
		_ = c.directory.pubsub.Close()
	}
	for _, id := range c.channels() {
		c.unwatch(id)
	}
	c.workers.Wait()
}
func (c *connection) heartbeat() {
	ticker := time.NewTicker(c.gateway.Limits.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.gateway.check(c.ctx, c.session); err != nil {
				c.cancel()
				return
			}
			ping, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			err := c.ws.Ping(ping)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
			alive, err := c.gateway.Broker.Renew(c.ctx, c.session.WorkspaceID, c.session.User.ID, c.id, c.gateway.Limits.Lease)
			if err != nil || !alive {
				c.cancel()
				return
			}
			for _, id := range c.channels() {
				if err := c.gateway.Chat.WithChannel(c.ctx, c.session, id, policy.PresenceRead, func(database.TenantTx, policy.Actor) error { return nil }); err != nil {
					c.cancel()
					return
				}
				if err := c.gateway.Broker.Online(c.ctx, c.session.WorkspaceID, id, c.session.User.ID, c.id, c.gateway.Limits.Lease, true); err != nil {
					c.cancel()
					return
				}
			}
		}
	}
}
