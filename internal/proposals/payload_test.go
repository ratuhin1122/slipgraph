package proposals

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/llm"
	"testing"
)

func TestPayloadAllowLists(t *testing.T) {
	id := uuid.NewString()
	base := `{"task_id":"` + id + `","version":1,"start":"2030-01-01T00:00:00Z","due":"2030-01-02T00:00:00Z"}`
	if _, _, err := Decode("reschedule_task", []byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{base[:len(base)-1] + `,"approved":true}`, base[:len(base)-1] + `,"workspace_id":"` + id + `"}`, `{"task_id":"` + id + `","version":0,"owner_id":null}`, `{"task_id":"` + id + `","version":1}`, `null`} {
		if _, _, err := Decode("change_assignee", []byte(input)); err == nil {
			t.Fatal("schema violation accepted")
		}
	}
	if _, _, err := Decode("execute_sql", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown executor accepted")
	}
}

func TestCreatePlanValidation(t *testing.T) {
	schema, err := Schema("create_plan")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := llm.Assemble([]string{"start 2030-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (llm.Demo{}).Complete(context.Background(), llm.Request{Prompt: prompt, Schema: schema, MaxTokens: 2048})
	if err != nil {
		t.Fatal(err)
	}
	executor, _, err := Decode("create_plan", result.JSON)
	if err != nil {
		t.Fatal(err)
	}
	plan := executor.(*CreatePlan)
	if !plan.Validate() {
		t.Fatal("valid plan rejected")
	}
	plan.Dependencies = append(plan.Dependencies, PlanEdge{TaskKey: "backend", BlockerKey: "launch"})
	if plan.Validate() {
		t.Fatal("cyclic plan accepted")
	}
	plan.Dependencies = plan.Dependencies[:2]
	plan.Tasks[1].Key = "backend"
	if plan.Validate() {
		t.Fatal("duplicate key accepted")
	}
	if _, _, err := Decode("create_plan", append(result.JSON[:len(result.JSON)-1], []byte(`,"approved":true}`)...)); err == nil {
		t.Fatal("model supplied approval")
	}
}
