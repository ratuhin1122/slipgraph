package policy

import (
	"context"
	"testing"
)

func TestMatrix(t *testing.T) {
	for _, role := range []Role{Admin, Member, Viewer, Consumer, "agent", "unknown"} {
		for _, action := range []Action{SessionRead, SessionWrite, WorkspaceCreate, WorkspaceRead, MemberRead, MemberInvite, MemberRoleChange, ProjectRead, ProjectWrite, TaskRead, TaskWrite, RiskRead, RiskCompute, EventConsume, AuditAppend, AuditVerify, "unknown"} {
			want := false
			switch role {
			case Admin:
				want = action == SessionRead || action == SessionWrite || action == WorkspaceCreate || action == WorkspaceRead || action == MemberRead || action == MemberInvite || action == MemberRoleChange || action == ProjectRead || action == ProjectWrite || action == TaskRead || action == TaskWrite || action == RiskRead || action == AuditVerify
			case Member:
				want = action == SessionRead || action == SessionWrite || action == WorkspaceCreate || action == WorkspaceRead || action == MemberRead || action == ProjectRead || action == ProjectWrite || action == TaskRead || action == TaskWrite || action == RiskRead
			case Viewer:
				want = action == SessionRead || action == SessionWrite || action == WorkspaceCreate || action == WorkspaceRead || action == MemberRead || action == ProjectRead || action == TaskRead || action == RiskRead
			case Consumer:
				want = action == EventConsume || action == AuditAppend || action == RiskCompute
			}
			owner := ""
			if action == MemberRoleChange {
				owner = "other"
			}
			actor := Actor{ID: "user", WorkspaceID: "a", Role: role}
			resource := Resource{WorkspaceID: "a", OwnerID: owner}
			if got := Can(context.Background(), actor, action, resource); got != want {
				t.Errorf("%s %s: got %v want %v", role, action, got, want)
			}
			resource.WorkspaceID = "b"
			if Can(context.Background(), actor, action, resource) {
				t.Errorf("%s %s crosses tenant", role, action)
			}
		}
	}
	if Can(context.Background(), Actor{ID: "me", WorkspaceID: "a", Role: Admin}, MemberRoleChange, Resource{WorkspaceID: "a", OwnerID: "me"}) {
		t.Fatal("self-role change permitted")
	}
	if Can(context.Background(), Actor{ID: "me", WorkspaceID: "a", Role: Admin}, SessionRead, Resource{WorkspaceID: "a", OwnerID: "other"}) {
		t.Fatal("other user's session permitted")
	}
}
