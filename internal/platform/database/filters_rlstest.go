//go:build rlstest

package database

import (
	"regexp"
	"strings"
)

// Fault injection is a compile-time test build, never an environment-controlled
// production bypass. Global identity queries retain mandatory ownership filters.
var workspacePredicate = regexp.MustCompile(`(?i)(?:[a-z_][a-z_0-9]*\.)?workspace_id\s*=\s*\$([0-9]+)`)

func tenantSQL(query string) string {
	if !strings.Contains(query, "app.") {
		return query
	}
	return workspacePredicate.ReplaceAllStringFunc(query, func(match string) string {
		return "($" + workspacePredicate.FindStringSubmatch(match)[1] + "::uuid IS NOT NULL)"
	})
}
