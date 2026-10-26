package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func violations(path, source string) []string {
	tree, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return []string{err.Error()}
	}
	normalized := filepath.ToSlash(path)
	isAuth := strings.Contains(normalized, "/internal/auth/")
	isDatabase := strings.Contains(normalized, "/internal/platform/database/")
	if strings.Contains(source, "Code generated") || isDatabase || (strings.Contains(normalized, "/internal/platform/testdb/") && strings.HasPrefix(source, "//go:build integration")) {
		return nil
	}
	var errors []string
	ast.Inspect(tree, func(node ast.Node) bool {
		if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			value, _ := strconv.Unquote(literal.Value)
			if strings.Contains(value, "identity.") && !isAuth {
				errors = append(errors, "identity schema is private to internal/auth")
			}
		}
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if !isAuth && (selector.Sel.Name == "Begin" || selector.Sel.Name == "BeginTx" || selector.Sel.Name == "Acquire" || selector.Sel.Name == "Connect") {
					errors = append(errors, "raw database connections/transactions are forbidden outside auth/database")
				}
				if len(call.Args) > 1 && (selector.Sel.Name == "Query" || selector.Sel.Name == "QueryRow" || selector.Sel.Name == "Exec") {
					if literal, ok := call.Args[1].(*ast.BasicLit); ok {
						query, _ := strconv.Unquote(literal.Value)
						if strings.Contains(strings.ToLower(query), "app.") {
							if receiver, ok := selector.X.(*ast.Ident); !ok || receiver.Name != "tx" {
								errors = append(errors, "tenant SQL requires WithTenant even inside auth")
							}
						}
					}
				}
				if !isAuth && len(call.Args) > 0 && (selector.Sel.Name == "Query" || selector.Sel.Name == "QueryRow" || selector.Sel.Name == "Exec") {
					// URL.Query has no arguments. Database calls must use the sealed
					// callback capability; receiver aliases cannot bypass this guard.
					if receiver, ok := selector.X.(*ast.Ident); !ok || receiver.Name != "tx" {
						errors = append(errors, "tenant query bypasses WithTenant")
					}
				}
				if selector.Sel.Name == "New" {
					if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "dbgen" {
						if len(call.Args) != 1 {
							errors = append(errors, "dbgen requires tenant capability")
						} else if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != "tx" {
							errors = append(errors, "dbgen must receive TenantTx")
						}
					}
				}
			}
		}
		return true
	})
	return errors
}
func TestDatabaseBoundaries(t *testing.T) {
	err := filepath.WalkDir("../../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == ".git" || entry.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.ToSlash(path), "/cmd/migrate/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, issue := range violations(path, string(data)) {
			t.Errorf("%s: %s", path, issue)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthCannotQueryTenantTablesThroughPool(t *testing.T) {
	source := `package auth;func bad(){pool.Query(ctx,"SELECT * FROM app.members")}`
	if len(violations("/repo/internal/auth/bad.go", source)) == 0 {
		t.Fatal("auth tenant bypass accepted")
	}
}

func TestGuardRejectsBypass(t *testing.T) {
	for _, source := range []string{
		`package tasks;func bad(){pool.QueryRow(ctx,"SELECT * FROM app.tasks")}`,
		`package tasks;func bad(){conn.Begin(ctx)}`,
		`package tasks;func bad(){aliased.Query(ctx,"SELECT * FROM app.tasks")}`,
		`package tasks;func bad(){dbgen.New(pool)}`,
		`package tasks;const query="SELECT * FROM identity.sessions"`,
	} {
		if len(violations("/repo/internal/tasks/bad.go", source)) == 0 {
			t.Fatal("guard accepted bypass")
		}
	}
}
