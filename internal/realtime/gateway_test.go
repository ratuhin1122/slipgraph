package realtime

import (
	"github.com/google/uuid"
	"testing"
)

func TestStrictFrames(t *testing.T) {
	id := uuid.NewString()
	for _, value := range []string{`{"type":"subscribe","channel_id":"` + id + `"}`, `{"type":"unsubscribe","channel_id":"` + id + `"}`, `{"type":"typing","channel_id":"` + id + `","active":true}`, `{"type":"heartbeat"}`} {
		if _, err := decode([]byte(value)); err != nil {
			t.Errorf("valid frame: %s: %v", value, err)
		}
	}
	for _, value := range []string{`{}`, `{"type":"unknown"}`, `{"type":"heartbeat","workspace_id":"` + id + `"}`, `{"type":"subscribe","channel_id":null}`, `{"type":"typing","channel_id":"` + id + `"}`, `{"type":"heartbeat"} {}`, `{"type":"subscribe","channel_id":"` + id + `","role":"admin"}`} {
		if _, err := decode([]byte(value)); err == nil {
			t.Errorf("unsafe frame accepted: %s", value)
		}
	}
}
