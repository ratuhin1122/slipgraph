package policy

import "context"

// Actor is populated only by trusted authentication/session middleware.
// No tenant route exists in the scaffold; future routes must resolve sessions first.
type Actor struct {
	ID          string
	WorkspaceID string
}
type Action string
type Resource struct{ WorkspaceID string }

const HealthRead Action = "health:read"

// Can is the single authorization boundary. Unknown actions fail closed.
// Liveness/readiness is intentionally public and returns no tenant data.
func Can(ctx context.Context, actor Actor, action Action, resource Resource) bool {
	if ctx.Err() != nil {
		return false
	}
	return action == HealthRead && actor == (Actor{}) && resource == (Resource{})
}
