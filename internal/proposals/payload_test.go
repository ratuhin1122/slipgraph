package proposals

import (
	"encoding/json"
	"github.com/google/uuid"
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
