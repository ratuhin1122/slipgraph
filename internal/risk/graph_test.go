package risk

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func base() time.Time       { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
func at(days int) time.Time { return base().Add(time.Duration(days) * 24 * time.Hour) }
func diamond() Plan {
	due := at(6)
	return Plan{Start: base(), LaunchDue: &due, Tasks: []Task{
		{ID: "A", Start: at(0), Due: at(2), EstimateMinutes: 2 * dayMinutes},
		{ID: "B", Start: at(0), Due: at(5), EstimateMinutes: 2 * dayMinutes},
		{ID: "C", Start: at(0), Due: at(5), EstimateMinutes: 3 * dayMinutes},
		{ID: "D", Start: at(0), Due: at(6), EstimateMinutes: dayMinutes, Milestone: true},
	}, Dependencies: []Dependency{{TaskID: "B", BlockerID: "A"}, {TaskID: "C", BlockerID: "A"}, {TaskID: "D", BlockerID: "B"}, {TaskID: "D", BlockerID: "C"}}}
}
func TestDiamondImpact(t *testing.T) {
	plan := diamond()
	delay := 2
	result, err := Analyze(plan, &Change{TaskID: "A", DelayDays: &delay})
	if err != nil {
		t.Fatal(err)
	}
	if result.BaselineFinish != at(6) || result.ForecastFinish != at(8) || result.ProjectSlipMinutes != 2*dayMinutes || result.LaunchSlipMinutes != 2*dayMinutes {
		t.Fatalf("wrong project impact: %+v", result)
	}
	if !reflect.DeepEqual(result.CriticalPath, []string{"A", "C", "D"}) {
		t.Fatal(result.CriticalPath)
	}
	expected := map[string][2]int{"A": {0, 4}, "B": {4, 6}, "C": {4, 7}, "D": {7, 8}}
	for _, impact := range result.Tasks {
		want := expected[impact.TaskID]
		if impact.EarliestStart != at(want[0]) || impact.EarliestFinish != at(want[1]) {
			t.Fatalf("%s: %+v", impact.TaskID, impact)
		}
		if !impact.Affected || impact.Risk != High {
			t.Fatal("expected risk on affected late tasks")
		}
		if impact.TaskID == "B" && impact.SlackConsumedMinutes != dayMinutes {
			t.Fatal("diamond short-path float was not consumed")
		}
		if impact.TaskID == "D" && impact.MilestoneSlipMinutes != 2*dayMinutes {
			t.Fatal("milestone slip missing")
		}
	}
	// Input slices and maps are never mutated; ordering does not change results.
	reversed := diamond()
	for i, j := 0, len(reversed.Tasks)-1; i < j; i, j = i+1, j-1 {
		reversed.Tasks[i], reversed.Tasks[j] = reversed.Tasks[j], reversed.Tasks[i]
	}
	for i, j := 0, len(reversed.Dependencies)-1; i < j; i, j = i+1, j-1 {
		reversed.Dependencies[i], reversed.Dependencies[j] = reversed.Dependencies[j], reversed.Dependencies[i]
	}
	other, err := Analyze(reversed, &Change{TaskID: "A", DelayDays: &delay})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, other) {
		t.Fatal("non-deterministic ordering")
	}
	if plan.Tasks[0].Due != at(2) {
		t.Fatal("input mutated")
	}
}
func TestCycleAndDisconnectedGraphs(t *testing.T) {
	p := diamond()
	g, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cycle, err := g.WouldCreateCycle("A", "D"); err != nil || !cycle {
		t.Fatal("cycle accepted")
	}
	if cycle, err := g.WouldCreateCycle("B", "C"); err != nil || cycle {
		t.Fatal("DAG edge rejected")
	}
	p.Dependencies = append(p.Dependencies, Dependency{TaskID: "A", BlockerID: "D"})
	if _, err := Compile(p); !errors.Is(err, ErrCycle) {
		t.Fatal("cycle not rejected")
	}
	p = diamond()
	p.Tasks = append(p.Tasks, Task{ID: "Z", Start: at(0), Due: at(5), EstimateMinutes: dayMinutes})
	delay := 1
	result, err := Analyze(p, &Change{TaskID: "C", DelayDays: &delay})
	if err != nil {
		t.Fatal(err)
	}
	for _, impact := range result.Tasks {
		if impact.TaskID == "Z" && impact.Affected {
			t.Fatal("unrelated task affected")
		}
	}
}
func TestDueDateAndCompletion(t *testing.T) {
	p := diamond()
	due := at(4)
	result, err := Analyze(p, &Change{TaskID: "A", NewDue: &due})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForecastFinish != at(8) {
		t.Fatal(result.ForecastFinish)
	}
	completed := at(1)
	p.Tasks[0].CompletedAt = &completed
	if _, err := Analyze(p, &Change{TaskID: "A", NewDue: &due}); !errors.Is(err, ErrInvalidGraph) {
		t.Fatal("completed task rescheduled")
	}
	if _, err := Analyze(p, nil); err != nil {
		t.Fatal(err)
	}
	p.Tasks[0].CompletedAt = nil
	p.Tasks[0].EstimateMinutes = -1
	if _, err := Analyze(p, nil); err == nil {
		t.Fatal("invalid duration accepted")
	}
	empty, err := Analyze(Plan{Start: base()}, nil)
	if err != nil || len(empty.Tasks) != 0 {
		t.Fatal("empty graph failed")
	}
}
func BenchmarkImpact10K(b *testing.B) {
	p := Plan{Start: base(), Tasks: make([]Task, 10000), Dependencies: make([]Dependency, 0, 9999)}
	for i := range p.Tasks {
		id := fmt.Sprintf("task-%05d", i)
		p.Tasks[i] = Task{ID: id, Start: base(), Due: base().Add(time.Duration(i+1) * time.Hour), EstimateMinutes: 60}
		if i > 0 {
			p.Dependencies = append(p.Dependencies, Dependency{TaskID: id, BlockerID: fmt.Sprintf("task-%05d", i-1)})
		}
	}
	days := 1
	change := Change{TaskID: "task-00000", DelayDays: &days}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Analyze(p, &change)
		if err != nil || len(result.Tasks) != 10000 {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if b.Elapsed()/time.Duration(b.N) > 200*time.Millisecond {
		b.Fatalf("10k impact exceeds 200ms: %s", b.Elapsed()/time.Duration(b.N))
	}
}

func TestForecastCriticalPathChangesBranches(t *testing.T) {
	delay := 3
	result, err := Analyze(diamond(), &Change{TaskID: "B", DelayDays: &delay})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.CriticalPath, []string{"A", "B", "D"}) {
		t.Fatalf("stale baseline path: %v", result.CriticalPath)
	}
	for _, task := range result.Tasks {
		expected := task.TaskID != "C"
		if task.Critical != expected {
			t.Fatalf("wrong forecast critical flag: %+v", task)
		}
	}
}
