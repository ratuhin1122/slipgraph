package proposals

import (
	"context"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/risk"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"strings"
	"time"
)

type PlanTask struct {
	Key             string    `json:"key"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Start           time.Time `json:"start"`
	Due             time.Time `json:"due"`
	EstimateMinutes int64     `json:"estimate_minutes"`
	Milestone       bool      `json:"milestone"`
}
type PlanEdge struct {
	TaskKey    string `json:"task_key"`
	BlockerKey string `json:"blocker_key"`
}
type CreatePlan struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Start        time.Time  `json:"start"`
	LaunchDue    *time.Time `json:"launch_due"`
	Tasks        []PlanTask `json:"tasks"`
	Dependencies []PlanEdge `json:"dependencies"`
}

func (t PlanTask) input() tasks.TaskInput {
	return tasks.TaskInput{Title: t.Title, Description: t.Description, Status: "todo", Start: t.Start, Due: t.Due, EstimateMinutes: t.EstimateMinutes, Milestone: t.Milestone}
}
func (p CreatePlan) Validate() bool {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 200 || len(p.Description) > 4000 || strings.ContainsRune(p.Name+p.Description, 0) || !validDates(p.Start, p.Start) || len(p.Tasks) < 1 || len(p.Tasks) > 20 || len(p.Dependencies) > 60 {
		return false
	}
	if p.LaunchDue != nil && !validDates(p.Start, *p.LaunchDue) {
		return false
	}
	graph := risk.Plan{Start: p.Start, LaunchDue: p.LaunchDue}
	seen := map[string]bool{}
	for _, t := range p.Tasks {
		if t.Key == "" || seen[t.Key] || !validDates(t.Start, t.Due) || t.Start.Before(p.Start) || !tasks.ValidInput(t.input()) || strings.TrimSpace(t.Title) == "" || strings.ContainsRune(t.Title+t.Description, 0) {
			return false
		}
		seen[t.Key] = true
		graph.Tasks = append(graph.Tasks, risk.Task{ID: t.Key, Start: t.Start, Due: t.Due, EstimateMinutes: t.EstimateMinutes, Milestone: t.Milestone})
	}
	edges := map[PlanEdge]bool{}
	for _, d := range p.Dependencies {
		if edges[d] {
			return false
		}
		edges[d] = true
		graph.Dependencies = append(graph.Dependencies, risk.Dependency{TaskID: d.TaskKey, BlockerID: d.BlockerKey})
	}
	_, err := risk.Compile(graph)
	return err == nil
}
func (p CreatePlan) Preview(ctx context.Context, tx database.TenantTx, a policy.Actor) (interface{}, interface{}, error) {
	if !p.Validate() {
		return nil, nil, auth.ErrInvalid
	}
	if !policy.Can(ctx, a, policy.ProjectWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return nil, nil, auth.ErrForbidden
	}
	return map[string]interface{}{}, p, nil
}
func (p CreatePlan) Execute(ctx context.Context, tx database.TenantTx, a policy.Actor) error {
	if _, _, err := p.Preview(ctx, tx, a); err != nil {
		return err
	}
	project, err := tasks.CreateProjectTx(ctx, tx, a, tasks.ProjectInput{Name: p.Name, Description: p.Description, Start: p.Start, LaunchDue: p.LaunchDue})
	if err != nil {
		return err
	}
	ids := map[string]uuid.UUID{}
	versions := map[string]int64{}
	for _, t := range p.Tasks {
		task, err := tasks.CreateTx(ctx, tx, a, project.ID, t.input())
		if err != nil {
			return err
		}
		ids[t.Key] = task.ID
		versions[t.Key] = task.Version
	}
	for _, d := range p.Dependencies {
		_, err := tasks.DependencyTx(ctx, tx, a, ids[d.TaskKey], ids[d.BlockerKey], versions[d.TaskKey], true)
		if err != nil {
			return err
		}
		versions[d.TaskKey]++
	}
	return nil
}
