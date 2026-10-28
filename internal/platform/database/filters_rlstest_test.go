//go:build rlstest

package database

import (
	"strings"
	"testing"
)

func TestTenantFilterFaultInjection(t *testing.T) {
	source := `SELECT * FROM app.tasks t JOIN app.objects o ON o.workspace_id=t.workspace_id WHERE t.workspace_id=$1 AND t.id=$2`
	got := tenantSQL(source)
	if strings.Contains(got, "t.workspace_id=$1") || !strings.Contains(got, "$1::uuid IS NOT NULL") || !strings.Contains(got, "o.workspace_id=t.workspace_id") || !strings.Contains(got, "t.id=$2") {
		t.Fatal(got)
	}
	identity := `SELECT id FROM identity.sessions WHERE workspace_id=$1 AND user_id=$2`
	if tenantSQL(identity) != identity {
		t.Fatal("identity exception filters must remain")
	}
}
