package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Broker struct{ Redis *redis.Client }
type Notice struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	ObjectID  string `json:"object_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Version   int64  `json:"version,omitempty"`
	Code      string `json:"code,omitempty"`
}

func prefix(workspace uuid.UUID) string { return workspace.String() + ":chat:" }
func topic(workspace, channel uuid.UUID) string {
	return prefix(workspace) + "channel:" + channel.String()
}
func directory(workspace uuid.UUID) string { return prefix(workspace) + "directory" }
func leaseKey(workspace, user uuid.UUID) string {
	return prefix(workspace) + "connections:" + user.String()
}

const redisNow = `local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000); `

func (b *Broker) Reserve(ctx context.Context, workspace, user, connection uuid.UUID, cap int, ttl time.Duration) (bool, error) {
	n, err := b.Redis.Eval(ctx, redisNow+`redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now); if redis.call('ZCARD',KEYS[1])>=tonumber(ARGV[2]) then return 0 end; redis.call('ZADD',KEYS[1],now+ARGV[3],ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[3]*2); return 1`, []string{leaseKey(workspace, user)}, connection.String(), cap, ttl.Milliseconds()).Int64()
	return n == 1, err
}
func (b *Broker) Renew(ctx context.Context, workspace, user, connection uuid.UUID, ttl time.Duration) (bool, error) {
	n, err := b.Redis.Eval(ctx, redisNow+`local expiry=redis.call('ZSCORE',KEYS[1],ARGV[1]); if not expiry or tonumber(expiry)<=now then return 0 end; redis.call('ZADD',KEYS[1],now+ARGV[2],ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[2]*2); return 1`, []string{leaseKey(workspace, user)}, connection.String(), ttl.Milliseconds()).Int64()
	return n == 1, err
}
func (b *Broker) Release(ctx context.Context, workspace, user, connection uuid.UUID) error {
	return b.Redis.ZRem(ctx, leaseKey(workspace, user), connection.String()).Err()
}
func (b *Broker) FrameAllowed(ctx context.Context, workspace, user uuid.UUID, limit int) (bool, error) {
	n, err := b.Redis.Eval(ctx, `local count=redis.call('INCR',KEYS[1]); if count==1 then redis.call('PEXPIRE',KEYS[1],10000) end; return count`, []string{prefix(workspace) + "frames:" + user.String()}).Int64()
	return n <= int64(limit), err
}
func presenceKey(workspace, channel uuid.UUID, kind string) string {
	return prefix(workspace) + kind + ":" + channel.String()
}
func (b *Broker) touch(ctx context.Context, workspace, channel, user, connection uuid.UUID, kind string, ttl time.Duration, active bool) error {
	member := user.String() + ":" + connection.String()
	return b.Redis.Eval(ctx, redisNow+`redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now); if ARGV[3]=='1' then redis.call('ZADD',KEYS[1],now+ARGV[2],ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[2]*2); else redis.call('ZREM',KEYS[1],ARGV[1]); end; return 1`, []string{presenceKey(workspace, channel, kind)}, member, ttl.Milliseconds(), boolInt(active)).Err()
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func (b *Broker) Online(ctx context.Context, workspace, channel, user, connection uuid.UUID, ttl time.Duration, active bool) error {
	return b.touch(ctx, workspace, channel, user, connection, "online", ttl, active)
}
func (b *Broker) Typing(ctx context.Context, workspace, channel, user, connection uuid.UUID, active bool) error {
	return b.touch(ctx, workspace, channel, user, connection, "typing", 5*time.Second, active)
}
func (b *Broker) NotifyPresence(ctx context.Context, workspace, channel uuid.UUID) error {
	return b.publish(ctx, topic(workspace, channel), Notice{Type: "presence", ChannelID: channel.String()})
}
func (b *Broker) Snapshot(ctx context.Context, workspace, channel uuid.UUID) (map[uuid.UUID]bool, map[uuid.UUID]bool, error) {
	sets := []map[uuid.UUID]bool{{}, {}}
	for i, kind := range []string{"online", "typing"} {
		values, err := b.Redis.Eval(ctx, redisNow+`redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now); return redis.call('ZRANGE',KEYS[1],0,999)`, []string{presenceKey(workspace, channel, kind)}).StringSlice()
		if err != nil {
			return nil, nil, err
		}
		for _, value := range values {
			user, _, _ := strings.Cut(value, ":")
			id, err := uuid.Parse(user)
			if err == nil {
				sets[i][id] = true
			}
		}
	}
	return sets[0], sets[1], nil
}
func (b *Broker) publish(ctx context.Context, key string, value Notice) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return b.Redis.Publish(ctx, key, data).Err()
}

// Consume publishes only invalidation IDs from a committed, tenant-scoped source.
// Redis publication may precede the receipt commit, so clients deduplicate event IDs
// and re-fetch canonical REST state after reconnect. No message text enters Redis.
func (b *Broker) Consume(ctx context.Context, tx database.TenantTx, actor policy.Actor, eventID uuid.UUID) error {
	if !policy.Can(ctx, actor, policy.ChatFanout, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("fanout denied")
	}
	var kind string
	var object uuid.UUID
	var version int64
	if err := tx.QueryRow(ctx, `SELECT event_type,object_id,version FROM app.events WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), eventID).Scan(&kind, &object, &version); err != nil {
		return err
	}
	notice := Notice{Type: "event", EventID: eventID.String(), ObjectID: object.String(), EventType: kind, Version: version}
	switch kind {
	case "channel.created":
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT c.id FROM app.channels c JOIN app.objects o ON o.id=c.id AND o.workspace_id=c.workspace_id WHERE c.workspace_id=$1 AND c.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), object).Scan(&id); err != nil {
			return err
		}
		notice.ChannelID = id.String()
		return b.publish(ctx, directory(tx.WorkspaceID()), notice)
	case "message.created", "message.updated", "message.deleted":
		var channel uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT c.id FROM app.messages m JOIN app.links l ON l.from_object=m.id AND l.workspace_id=m.workspace_id AND l.link_type='belongs_to' JOIN app.channels c ON c.id=l.to_object AND c.workspace_id=m.workspace_id WHERE m.workspace_id=$1 AND m.id=$2`, tx.WorkspaceID(), object).Scan(&channel); err != nil {
			return err
		}
		notice.ChannelID = channel.String()
		return b.publish(ctx, topic(tx.WorkspaceID(), channel), notice)
	default:
		return nil
	}
}
