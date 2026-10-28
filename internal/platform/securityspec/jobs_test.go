package securityspec

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRegisteredJobsHaveExecutedSecurityCases(t *testing.T) {
	data, e := os.ReadFile("../../../api/job-security-coverage.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []string
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	registered := map[string]bool{}
	for _, name := range cases {
		if registered[name] {
			t.Fatal("duplicate job security case", name)
		}
		registered[name] = true
	}
	tree, e := parser.ParseFile(token.NewFileSet(), "../service/service.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	ast.Inspect(tree, func(node ast.Node) bool {
		kv, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			return true
		}
		value, ok := kv.Value.(*ast.BasicLit)
		if !ok || value.Kind != token.STRING {
			return true
		}
		name, e := strconv.Unquote(value.Value)
		if e != nil {
			t.Fatal(e)
		}
		if !registered["consumer/"+name] {
			t.Errorf("registered consumer has no security case: %s", name)
		}
		return true
	})
	files, e := filepath.Glob("../../proposals/schemas/*.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range files {
		kind := strings.TrimSuffix(filepath.Base(path), ".json")
		if !registered["generation/"+kind] && !registered["executor/"+kind] {
			t.Errorf("new schema needs an executed job security case: %s", kind)
		}
	}
	payload, e := parser.ParseFile(token.NewFileSet(), "../../proposals/payload.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, decl := range payload.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Decode" {
			continue
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				literal, ok := expr.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				kind, e := strconv.Unquote(literal.Value)
				if e != nil {
					t.Fatal(e)
				}
				if !registered["executor/"+kind] {
					t.Errorf("executable kind has no security case: %s", kind)
				}
			}
			return true
		})
	}
}
