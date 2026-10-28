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

func TestChatMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, "unknown"} {
		for _, action := range []Action{ChannelRead, ChannelCreate, MessagePost, MessageEdit, MessageDelete, RealtimeConnect, RealtimeSubscribe, PresenceRead, TypingWrite, ChatFanout} {
			for _, owner := range []string{"me", "other"} {
				actor := Actor{ID: "me", WorkspaceID: "a", Role: role}
				resource := Resource{WorkspaceID: "a", OwnerID: owner}
				want := false
				switch action {
				case ChannelRead, RealtimeConnect, RealtimeSubscribe, PresenceRead:
					want = role == Admin || role == Member || role == Viewer
				case ChannelCreate, MessagePost, TypingWrite:
					want = role == Admin || role == Member
				case MessageEdit, MessageDelete:
					want = (role == Admin || role == Member) && owner == "me"
				case ChatFanout:
					want = role == Consumer
				}
				if Can(context.Background(), actor, action, resource) != want {
					t.Errorf("role %s action %s owner %s", role, action, owner)
				}
				resource.WorkspaceID = "b"
				if Can(context.Background(), actor, action, resource) {
					t.Errorf("cross tenant %s %s", role, action)
				}
			}
		}
	}
}

func TestProposalMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, "unknown"} {
		for _, action := range []Action{ProposalRead, ProposalDecide, ProposalExecute, AgentRequest, AgentRun} {
			want := false
			switch action {
			case ProposalRead:
				want = role == Admin || role == Member || role == Viewer
			case ProposalDecide, AgentRequest:
				want = role == Admin || role == Member
			case ProposalExecute, AgentRun:
				want = role == Consumer
			}
			a := Actor{ID: "id", WorkspaceID: "a", Role: role}
			if Can(context.Background(), a, action, Resource{WorkspaceID: "a"}) != want {
				t.Errorf("%s %s", role, action)
			}
			if Can(context.Background(), a, action, Resource{WorkspaceID: "b"}) {
				t.Fatal("cross-tenant proposal policy")
			}
		}
	}
}

func TestMeetingPermissionMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, Role("unknown")} {
		actor := Actor{ID: "actor", WorkspaceID: "workspace", Role: role}
		resource := Resource{WorkspaceID: "workspace"}
		if got := Can(context.Background(), actor, MeetingRead, resource); got != (role == Admin || role == Member || role == Viewer) {
			t.Fatalf("read role %s", role)
		}
		for _, action := range []Action{MeetingWrite, DecisionWrite} {
			if got := Can(context.Background(), actor, action, resource); got != (role == Admin || role == Member) {
				t.Fatalf("write role %s", role)
			}
		}
		if got := Can(context.Background(), actor, MeetingTranscribe, resource); got != (role == Consumer) {
			t.Fatalf("job role %s", role)
		}
		if Can(context.Background(), actor, MeetingRead, Resource{WorkspaceID: "foreign"}) {
			t.Fatal("cross-workspace policy accepted")
		}
	}
}

func TestSearchMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, "unknown"} {
		for _, action := range []Action{SearchRead, SearchAsk, SearchIndex} {
			actor := Actor{ID: "me", WorkspaceID: "a", Role: role}
			want := (action == SearchIndex && role == Consumer) || (action != SearchIndex && (role == Admin || role == Member || role == Viewer))
			if Can(context.Background(), actor, action, Resource{WorkspaceID: "a"}) != want {
				t.Errorf("role %s action %s", role, action)
			}
			if Can(context.Background(), actor, action, Resource{WorkspaceID: "b"}) {
				t.Fatal("cross-tenant search permission")
			}
		}
	}
}

func TestHandlerPreflightDoesNotGrantOwnership(t *testing.T) {
	a := Actor{ID: "me", WorkspaceID: "a", Role: Admin}
	if !MayAttempt(context.Background(), a, MemberRoleChange, "a") || Can(context.Background(), a, MemberRoleChange, Resource{WorkspaceID: "a", OwnerID: "me"}) {
		t.Fatal("role preflight bypassed own-role denial")
	}
	if MayAttempt(context.Background(), a, "unknown", "a") || MayAttempt(context.Background(), a, TaskRead, "b") {
		t.Fatal("preflight failed open")
	}
}

func TestIntegrationMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, "unknown"} {
		for _, action := range []Action{IntegrationRead, IntegrationManage, IntegrationReceive} {
			a := Actor{ID: "me", WorkspaceID: "a", Role: role}
			want := role == Admin && action != IntegrationReceive || role == Consumer && action == IntegrationReceive
			if Can(context.Background(), a, action, Resource{WorkspaceID: "a"}) != want {
				t.Fatalf("%s %s", role, action)
			}
			if Can(context.Background(), a, action, Resource{WorkspaceID: "b"}) {
				t.Fatal("foreign integration permission")
			}
		}
	}
}
