package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"strconv"
	"strings"
	"time"

	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

var ErrWebhook = errors.New("webhook verification failed")

// VerifySignature binds timestamp and delivery ID as well as the body. A future
// vendor adapter must implement that vendor's exact signature contract first.
func VerifySignature(key []byte, workspace uuid.UUID, provider, delivery, timestamp, signature string, body []byte, now time.Time) error {
	if workspace == uuid.Nil || len(key) < 32 || len(provider) < 1 || len(provider) > 100 || len(delivery) < 1 || len(delivery) > 200 || strings.ContainsAny(provider+delivery, "\r\n.") || len(body) > 1<<20 {
		return ErrWebhook
	}
	seconds, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil {
		return ErrWebhook
	}
	if seconds < now.Add(-5*time.Minute).Unix() || seconds > now.Add(30*time.Second).Unix() {
		return ErrWebhook
	}
	supplied, e := hex.DecodeString(signature)
	if e != nil || len(supplied) != sha256.Size {
		return ErrWebhook
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(workspace.String() + "." + provider + "." + delivery + "." + timestamp + "."))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), supplied) {
		return ErrWebhook
	}
	return nil
}

// VerifyAndClaim is transaction-scoped, service-authorized and single use even
// across concurrent replicas. Workspace comes exclusively from the verified Tx.
// No HTTP ingress is exposed by this primitive.
func VerifyAndClaim(ctx context.Context, tx database.TenantTx, a policy.Actor, key []byte, provider, delivery, timestamp, signature string, body []byte, now time.Time) error {
	if !policy.Can(ctx, a, policy.EventConsume, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return ErrWebhook
	}
	if e := VerifySignature(key, tx.WorkspaceID(), provider, delivery, timestamp, signature, body, now); e != nil {
		return e
	}
	digest := sha256.Sum256(body)
	tag, e := tx.Exec(ctx, `INSERT INTO app.webhook_receipts(workspace_id,provider,delivery_id,body_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tx.WorkspaceID(), provider, delivery, hex.EncodeToString(digest[:]))
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrWebhook
	}
	return nil
}
