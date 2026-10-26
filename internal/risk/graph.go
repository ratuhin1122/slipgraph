// Package risk implements deterministic finish-to-start calendar scheduling.
package risk

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrCycle = errors.New("dependency cycle")
var ErrInvalidGraph = errors.New("invalid dependency graph")

const dayMinutes int64 = 24 * 60

const AlgorithmVersion = "calendar-fs-cpm-v1"

type Task struct {
	FinishNotBefore   *time.Time `json:"finish_not_before,omitempty"`
	ID                string     `json:"id"`
	Start             time.Time  `json:"start"`
	Due               time.Time  `json:"due"`
	EstimateMinutes   int64      `json:"estimate_minutes"`
	Milestone         bool       `json:"milestone"`
	ReportedDelayDays int        `json:"reported_delay_days"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

// Dependency corresponds to links(from_object=TaskID,to_object=BlockerID,
// link_type=blocked_by). Scheduling traverses blocker -> dependent.
type Dependency struct {
	TaskID    string `json:"task_id"`
	BlockerID string `json:"blocker_id"`
}
type Plan struct {
	Start        time.Time
	LaunchDue    *time.Time
	Tasks        []Task
	Dependencies []Dependency
}
type Change struct {
	TaskID    string
	DelayDays *int
	NewDue    *time.Time
}
type Level string

const (
	None   Level = "none"
	Low    Level = "low"
	Medium Level = "medium"
	High   Level = "high"
)

type Impact struct {
	TaskID                string    `json:"task_id"`
	BaselineStart         time.Time `json:"baseline_start"`
	BaselineFinish        time.Time `json:"baseline_finish"`
	EarliestStart         time.Time `json:"earliest_start"`
	EarliestFinish        time.Time `json:"earliest_finish"`
	SlipMinutes           int64     `json:"slip_minutes"`
	SlackConsumedMinutes  int64     `json:"slack_consumed_minutes"`
	RemainingSlackMinutes int64     `json:"remaining_slack_minutes"`
	Milestone             bool      `json:"milestone"`
	MilestoneSlipMinutes  int64     `json:"milestone_slip_minutes"`
	Critical              bool      `json:"critical"`
	Affected              bool      `json:"affected"`
	Risk                  Level     `json:"risk"`
}
type Assessment struct {
	AlgorithmVersion   string    `json:"algorithm_version"`
	BaselineFinish     time.Time `json:"baseline_finish"`
	ForecastFinish     time.Time `json:"forecast_finish"`
	ProjectSlipMinutes int64     `json:"project_slip_minutes"`
	LaunchSlipMinutes  int64     `json:"launch_slip_minutes"`
	CriticalPath       []string  `json:"critical_path"`
	Tasks              []Impact  `json:"tasks"`
}
type Graph struct {
	plan           Plan
	nodes          []Task
	index          map[string]int
	next, previous [][]int
	order          []int
	origin         int64
}

func minute(t time.Time) int64   { return t.UTC().Unix() / 60 }
func stamp(v int64) time.Time    { return time.Unix(v*60, 0).UTC() }
func validDate(t time.Time) bool { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }
func Compile(plan Plan) (*Graph, error) {
	if !validDate(plan.Start) || len(plan.Tasks) > 100000 || len(plan.Dependencies) > 1000000 {
		return nil, ErrInvalidGraph
	}
	if plan.LaunchDue != nil && !validDate(*plan.LaunchDue) {
		return nil, ErrInvalidGraph
	}
	g := &Graph{plan: plan, nodes: append([]Task(nil), plan.Tasks...), index: make(map[string]int, len(plan.Tasks)), origin: minute(plan.Start)}
	sort.Slice(g.nodes, func(i, j int) bool { return g.nodes[i].ID < g.nodes[j].ID })
	n := len(g.nodes)
	g.next = make([][]int, n)
	g.previous = make([][]int, n)
	indegree := make([]int, n)
	for i, t := range g.nodes {
		if t.ID == "" || !validDate(t.Start) || !validDate(t.Due) || t.Due.Before(t.Start) || (t.EstimateMinutes < 0 || (t.EstimateMinutes == 0 && !t.Milestone)) || t.EstimateMinutes > 3650*dayMinutes || t.ReportedDelayDays < 0 || t.ReportedDelayDays > 3650 {
			return nil, fmt.Errorf("%w: task", ErrInvalidGraph)
		}
		if t.CompletedAt != nil && !validDate(*t.CompletedAt) {
			return nil, ErrInvalidGraph
		}
		if _, exists := g.index[t.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate task", ErrInvalidGraph)
		}
		g.index[t.ID] = i
	}
	edges := make(map[[2]int]struct{}, len(plan.Dependencies))
	for _, edge := range plan.Dependencies {
		dependent, ok := g.index[edge.TaskID]
		if !ok {
			return nil, fmt.Errorf("%w: missing dependent", ErrInvalidGraph)
		}
		blocker, ok := g.index[edge.BlockerID]
		if !ok {
			return nil, fmt.Errorf("%w: missing blocker", ErrInvalidGraph)
		}
		key := [2]int{blocker, dependent}
		if _, exists := edges[key]; exists {
			continue
		}
		edges[key] = struct{}{}
		g.next[blocker] = append(g.next[blocker], dependent)
		g.previous[dependent] = append(g.previous[dependent], blocker)
		indegree[dependent]++
	}
	queue := make([]int, 0, n)
	for i, d := range indegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		g.order = append(g.order, node)
		for _, child := range g.next[node] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if len(g.order) != n {
		return nil, ErrCycle
	}
	return g, nil
}

// WouldCreateCycle uses an iterative search, so a 10k-node chain cannot overflow
// the call stack. Existing duplicate edges are harmless to graph reachability.
func (g *Graph) WouldCreateCycle(taskID, blockerID string) (bool, error) {
	from, ok := g.index[taskID]
	if !ok {
		return false, ErrInvalidGraph
	}
	target, ok := g.index[blockerID]
	if !ok {
		return false, ErrInvalidGraph
	}
	if from == target {
		return true, nil
	}
	visited := make([]bool, len(g.nodes))
	queue := []int{from}
	visited[from] = true
	for head := 0; head < len(queue); head++ {
		for _, node := range g.next[queue[head]] {
			if node == target {
				return true, nil
			}
			if !visited[node] {
				visited[node] = true
				queue = append(queue, node)
			}
		}
	}
	return false, nil
}
func Analyze(plan Plan, change *Change) (Assessment, error) {
	g, err := Compile(plan)
	if err != nil {
		return Assessment{}, err
	}
	return g.Impact(change)
}
func (g *Graph) schedule(forecast bool, change *Change) ([]int64, []int64, []int, int64, error) {
	n := len(g.nodes)
	start, finish := make([]int64, n), make([]int64, n)
	parents := make([]int, n)
	end := g.origin
	for _, i := range g.order {
		task := g.nodes[i]
		parents[i] = -1
		if task.CompletedAt != nil {
			start[i] = minute(*task.CompletedAt)
			finish[i] = start[i]
			end = max(end, finish[i])
			continue
		}
		start[i] = max(g.origin, minute(task.Start))
		for _, p := range g.previous[i] {
			if finish[p] > start[i] || (finish[p] == start[i] && (parents[i] < 0 || p < parents[i])) {
				start[i] = finish[p]
				parents[i] = p
			}
		}
		delay := int64(0)
		if forecast {
			delay = int64(task.ReportedDelayDays) * dayMinutes
		}
		if forecast && change != nil && change.TaskID == task.ID && change.DelayDays != nil {
			delay = int64(*change.DelayDays) * dayMinutes
		}
		finish[i] = start[i] + task.EstimateMinutes + delay
		if forecast && task.FinishNotBefore != nil {
			finish[i] = max(finish[i], minute(*task.FinishNotBefore)+delay)
		}
		if forecast && change != nil && change.TaskID == task.ID && change.NewDue != nil {
			finish[i] = max(finish[i], minute(*change.NewDue))
		}
		if finish[i] > minute(time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)) {
			return nil, nil, nil, 0, ErrInvalidGraph
		}
		end = max(end, finish[i])
	}
	return start, finish, parents, end, nil
}
func (g *Graph) latest(end int64, durations []int64) []int64 {
	latest := make([]int64, len(g.nodes))
	for j := len(g.order) - 1; j >= 0; j-- {
		i := g.order[j]
		finish := end
		for _, child := range g.next[i] {
			if g.nodes[child].CompletedAt == nil {
				finish = min(finish, latest[child])
			}
		}
		duration := g.nodes[i].EstimateMinutes
		if durations != nil {
			duration = durations[i]
		}
		latest[i] = finish - duration
	}
	return latest
}
func (g *Graph) Impact(change *Change) (Assessment, error) {
	result := Assessment{AlgorithmVersion: AlgorithmVersion, CriticalPath: []string{}, Tasks: make([]Impact, 0, len(g.nodes))}
	if change != nil {
		index, ok := g.index[change.TaskID]
		if !ok || g.nodes[index].CompletedAt != nil || (change.DelayDays == nil) == (change.NewDue == nil) {
			return result, ErrInvalidGraph
		}
		if change.DelayDays != nil && (*change.DelayDays < 0 || *change.DelayDays > 3650) {
			return result, ErrInvalidGraph
		}
		if change.NewDue != nil && !validDate(*change.NewDue) {
			return result, ErrInvalidGraph
		}
	}
	baseStart, baseFinish, _, baseEnd, err := g.schedule(false, nil)
	if err != nil {
		return result, err
	}
	nextStart, nextFinish, parent, nextEnd, err := g.schedule(true, change)
	if err != nil {
		return result, err
	}
	result.BaselineFinish = stamp(baseEnd)
	result.ForecastFinish = stamp(nextEnd)
	result.ProjectSlipMinutes = max(0, nextEnd-baseEnd)
	target := baseEnd
	if g.plan.LaunchDue != nil {
		target = minute(*g.plan.LaunchDue)
	}
	result.LaunchSlipMinutes = max(0, nextEnd-target)
	forecastDurations := make([]int64, len(g.nodes))
	for i := range forecastDurations {
		forecastDurations[i] = nextFinish[i] - nextStart[i]
	}
	criticalLatest := g.latest(nextEnd, forecastDurations)
	deadlineLatest := g.latest(target, nil)
	endNode := -1
	for i, t := range g.nodes {
		if t.CompletedAt == nil && nextFinish[i] == nextEnd && (endNode < 0 || i < endNode) {
			endNode = i
		}
	}
	for current := endNode; current >= 0; current = parent[current] {
		result.CriticalPath = append(result.CriticalPath, g.nodes[current].ID)
	}
	for left, right := 0, len(result.CriticalPath)-1; left < right; left, right = left+1, right-1 {
		result.CriticalPath[left], result.CriticalPath[right] = result.CriticalPath[right], result.CriticalPath[left]
	}
	for i, t := range g.nodes {
		due := minute(t.Due)
		if change != nil && change.TaskID == t.ID && change.NewDue != nil {
			due = minute(*change.NewDue)
		}
		slack := max(0, min(minute(t.Due)-baseFinish[i], deadlineLatest[i]-baseStart[i]))
		remaining := min(due-nextFinish[i], deadlineLatest[i]-nextStart[i]-(nextFinish[i]-nextStart[i]-t.EstimateMinutes))
		impact := Impact{TaskID: t.ID, BaselineStart: stamp(baseStart[i]), BaselineFinish: stamp(baseFinish[i]), EarliestStart: stamp(nextStart[i]), EarliestFinish: stamp(nextFinish[i]), SlipMinutes: nextFinish[i] - baseFinish[i], SlackConsumedMinutes: max(0, slack-max(0, remaining)), RemainingSlackMinutes: remaining, Milestone: t.Milestone, Critical: criticalLatest[i] == nextStart[i], Affected: nextStart[i] != baseStart[i] || nextFinish[i] != baseFinish[i], Risk: None}
		if t.Milestone {
			impact.MilestoneSlipMinutes = max(0, nextFinish[i]-due)
		}
		if t.CompletedAt != nil {
			impact.Critical = false
			impact.Affected = false
			impact.SlackConsumedMinutes = 0
			impact.RemainingSlackMinutes = 0
		} else if nextFinish[i] > due || (result.LaunchSlipMinutes > 0 && impact.Critical && impact.Affected) {
			impact.Risk = High
		} else if impact.Affected && (impact.Critical || impact.RemainingSlackMinutes <= dayMinutes || impact.SlackConsumedMinutes > 0) {
			impact.Risk = Medium
		} else if impact.Affected {
			impact.Risk = Low
		}
		result.Tasks = append(result.Tasks, impact)
	}
	return result, nil
}
