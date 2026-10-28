package realtime

import "testing"

func TestFramesRejectCallerSelectedWorkspace(t *testing.T) {
	for _, frame := range []string{
		`{"type":"heartbeat","workspace_id":"foreign"}`,
		`{"type":"subscribe","workspace_id":"foreign","channel_id":"019a1111-1111-7111-8111-111111111111"}`,
		`{"type":"typing","workspace_id":"foreign","channel_id":"019a1111-1111-7111-8111-111111111111","active":true}`,
		`{"type":"heartbeat","channel_id":"019a1111-1111-7111-8111-111111111111"}`,
	} {
		if _, e := decode([]byte(frame)); e == nil {
			t.Fatal("caller-selected scope accepted")
		}
	}
}
