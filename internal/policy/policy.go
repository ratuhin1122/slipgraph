package policy

import "context"

type Role string

const (
	Admin    Role = "admin"
	Member   Role = "member"
	Viewer   Role = "viewer"
	Consumer Role = "consumer"
)

type Actor struct {
	ID          string
	WorkspaceID string
	Role        Role
}
type Resource struct {
	WorkspaceID string
	OwnerID     string
}
type Action string

const (
	HealthRead       Action = "health:read"
	AuthPublic       Action = "auth:public"
	SessionRead      Action = "session:read"
	SessionWrite     Action = "session:write"
	WorkspaceCreate  Action = "workspace:create"
	WorkspaceRead    Action = "workspace:read"
	MemberRead       Action = "member:read"
	MemberInvite     Action = "member:invite"
	MemberRoleChange Action = "member:role_change"
	ProjectRead      Action = "project:read"
	ProjectWrite     Action = "project:write"
	TaskRead         Action = "task:read"
	TaskWrite        Action = "task:write"
	RiskRead         Action = "risk:read"
	RiskCompute      Action = "risk:compute"
	AuditAppend      Action = "audit:append"
	AuditVerify      Action = "audit:verify"
	EventConsume     Action = "event:consume"
)

// Can is the single authorization boundary. Actors originate in verified sessions;
// consumer actors originate in authenticated, tenant-scoped service sessions.
func Can(ctx context.Context, actor Actor, action Action, resource Resource) bool {
	if ctx.Err() != nil {
		return false
	}
	if action == HealthRead || action == AuthPublic {
		return actor == (Actor{}) && resource == (Resource{})
	}
	if actor.ID == "" || actor.WorkspaceID == "" || actor.WorkspaceID != resource.WorkspaceID {
		return false
	}
	if actor.Role == Consumer {
		return action == EventConsume || action == AuditAppend || action == RiskCompute
	}
	if actor.Role != Admin && actor.Role != Member && actor.Role != Viewer {
		return false
	}
	switch action {
	case SessionRead, SessionWrite:
		return resource.OwnerID == "" || resource.OwnerID == actor.ID
	case WorkspaceCreate, WorkspaceRead, MemberRead, ProjectRead, TaskRead, RiskRead:
		return true
	case MemberInvite, AuditVerify:
		return actor.Role == Admin
	case MemberRoleChange:
		return actor.Role == Admin && resource.OwnerID != "" && resource.OwnerID != actor.ID
	case ProjectWrite, TaskWrite:
		return actor.Role == Admin || actor.Role == Member
	default:
		return false
	}
}
