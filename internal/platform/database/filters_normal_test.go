//go:build !rlstest

package database

import "testing"

func TestProductionSQLFiltersRemainIntact(t *testing.T) {
	query := `SELECT * FROM app.tasks WHERE workspace_id=$1 AND id=$2`
	if tenantSQL(query) != query {
		t.Fatal("test fault injection enabled in normal build")
	}
}
