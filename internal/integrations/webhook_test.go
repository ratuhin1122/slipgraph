package integrations

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"strconv"
	"testing"
	"time"
)

func TestWebhookSignatureBindingAndExpiry(t *testing.T) {
	workspace := uuid.New()
	key := bytes.Repeat([]byte{1}, 32)
	now := time.Unix(1700000000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte("fixture")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(workspace.String() + ".fixture.delivery-1." + ts + "."))
	_, _ = mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if e := VerifySignature(key, workspace, "fixture", "delivery-1", ts, sig, body, now); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		provider, id, stamp, sig string
		body                     []byte
		at                       time.Time
	}{
		{"other", "delivery-1", ts, sig, body, now}, {"fixture", "delivery-2", ts, sig, body, now}, {"fixture", "delivery-1", ts, sig, []byte("tampered"), now},
		{"fixture", "delivery-1", ts, sig, body, now.Add(6 * time.Minute)}, {"fixture", "delivery-1", ts, sig, body, now.Add(-time.Minute)},
		{"fixture", "delivery-1", ts, "bad", body, now},
	} {
		if VerifySignature(key, workspace, tc.provider, tc.id, tc.stamp, tc.sig, tc.body, tc.at) == nil {
			t.Fatal("unbound webhook accepted")
		}
	}
}
