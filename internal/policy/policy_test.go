package policy

import (
	"context"
	"testing"
)

func TestDenyByDefault(t *testing.T) {
	if Can(context.Background(), Actor{ID: "agent", WorkspaceID: "a"}, "objects:write", Resource{WorkspaceID: "b"}) {
		t.Fatal("unauthorized cross-tenant action")
	}
	if Can(context.Background(), Actor{}, "unknown", Resource{}) {
		t.Fatal("unknown action allowed")
	}
	if !Can(context.Background(), Actor{}, HealthRead, Resource{}) {
		t.Fatal("public health denied")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Can(ctx, Actor{}, HealthRead, Resource{}) {
		t.Fatal("cancelled request allowed")
	}
}
