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
	SearchRead        Action = "search:read"
	SearchAsk         Action = "search:ask"
	SearchIndex       Action = "search:index"
	MeetingRead       Action = "meeting:read"
	MeetingWrite      Action = "meeting:write"
	MeetingTranscribe Action = "meeting:transcribe"
	DecisionWrite     Action = "decision:write"
	ProposalRead      Action = "proposal:read"
	ProposalDecide    Action = "proposal:decide"
	ProposalExecute   Action = "proposal:execute"
	AgentRequest      Action = "agent:request"
	AgentRun          Action = "agent:run"
	ChannelRead       Action = "channel:read"
	ChannelCreate     Action = "channel:create"
	MessagePost       Action = "message:post"
	MessageEdit       Action = "message:edit"
	MessageDelete     Action = "message:delete"
	RealtimeConnect   Action = "realtime:connect"
	RealtimeSubscribe Action = "realtime:subscribe"
	PresenceRead      Action = "presence:read"
	TypingWrite       Action = "typing:write"
	ChatFanout        Action = "chat:fanout"
	HealthRead        Action = "health:read"
	AuthPublic        Action = "auth:public"
	SessionRead       Action = "session:read"
	SessionWrite      Action = "session:write"
	WorkspaceCreate   Action = "workspace:create"
	WorkspaceRead     Action = "workspace:read"
	MemberRead        Action = "member:read"
	MemberInvite      Action = "member:invite"
	MemberRoleChange  Action = "member:role_change"
	ProjectRead       Action = "project:read"
	ProjectWrite      Action = "project:write"
	TaskRead          Action = "task:read"
	TaskWrite         Action = "task:write"
	RiskRead          Action = "risk:read"
	RiskCompute       Action = "risk:compute"
	AuditAppend       Action = "audit:append"
	AuditVerify       Action = "audit:verify"
	EventConsume      Action = "event:consume"
)

// Can is the single authorization boundary. Actors originate in verified sessions;
// consumer actors originate in authenticated, tenant-scoped service sessions.
// MayAttempt checks only the role/action matrix for handler preflight. It never
// grants ownership of an object; Can must still run with the actual resource.
func MayAttempt(ctx context.Context, actor Actor, action Action, workspaceID string) bool {
	resource := Resource{WorkspaceID: workspaceID}
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
		return action == SearchIndex || action == EventConsume || action == AuditAppend || action == RiskCompute || action == ChatFanout || action == ProposalExecute || action == AgentRun || action == MeetingTranscribe
	}
	if actor.Role != Admin && actor.Role != Member && actor.Role != Viewer {
		return false
	}
	switch action {
	case SessionRead, SessionWrite:
		return true
	case SearchRead, SearchAsk, MeetingRead, ProposalRead, WorkspaceCreate, WorkspaceRead, MemberRead, ProjectRead, TaskRead, RiskRead, ChannelRead, RealtimeConnect, RealtimeSubscribe, PresenceRead:
		return true
	case MemberInvite, AuditVerify:
		return actor.Role == Admin
	case MemberRoleChange:
		return actor.Role == Admin
	case MessageEdit, MessageDelete:
		return actor.Role == Admin || actor.Role == Member
	case MeetingWrite, DecisionWrite, ProposalDecide, AgentRequest, ProjectWrite, TaskWrite, ChannelCreate, MessagePost, TypingWrite:
		return actor.Role == Admin || actor.Role == Member
	default:
		return false
	}
}

func Can(ctx context.Context, actor Actor, action Action, resource Resource) bool {
	if !MayAttempt(ctx, actor, action, resource.WorkspaceID) {
		return false
	}
	switch action {
	case HealthRead, AuthPublic:
		return resource == (Resource{})
	case SessionRead, SessionWrite:
		return resource.OwnerID == "" || resource.OwnerID == actor.ID
	case MemberRoleChange:
		return resource.OwnerID != "" && resource.OwnerID != actor.ID
	case MessageEdit, MessageDelete:
		return resource.OwnerID == actor.ID
	default:
		return true
	}
}
