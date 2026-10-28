package securityspec

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRequestContractsCannotSelectTenantIdentity(t *testing.T) {
	data, e := os.ReadFile("../../../api/openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]interface{}
	if e = yaml.Unmarshal(data, &doc); e != nil {
		t.Fatal(e)
	}
	normalize := func(s string) string { return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s)) }
	forbidden := func(s string) bool {
		switch normalize(s) {
		case "workspaceid", "xworkspaceid", "tenantid", "xtenantid":
			return true
		}
		return false
	}
	var walk func(interface{}, map[string]bool)
	walk = func(value interface{}, seen map[string]bool) {
		switch value := value.(type) {
		case []interface{}:
			for _, child := range value {
				walk(child, seen)
			}
		case map[string]interface{}:
			if ref, ok := value["$ref"].(string); ok {
				if seen[ref] {
					return
				}
				seen[ref] = true
				var target interface{} = doc
				for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]interface{})
					if !ok {
						t.Fatal("unresolved contract reference", ref)
					}
					target = object[part]
				}
				walk(target, seen)
			}
			if properties, ok := value["properties"].(map[string]interface{}); ok {
				for name := range properties {
					if forbidden(name) {
						t.Errorf("request schema accepts authoritative tenant identity: %s", name)
					}
				}
			}
			if name, ok := value["name"].(string); ok && forbidden(name) {
				t.Errorf("request parameter accepts tenant identity: %s", name)
			}
			for key, child := range value {
				if key != "$ref" {
					walk(child, seen)
				}
			}
		}
	}
	paths, ok := doc["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("missing paths")
	}
	for _, item := range paths {
		operations, ok := item.(map[string]interface{})
		if !ok {
			t.Fatal("invalid path item")
		}
		for _, value := range operations {
			operation, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			if body, ok := operation["requestBody"]; ok {
				walk(body, map[string]bool{})
			}
			if parameters, ok := operation["parameters"]; ok {
				walk(parameters, map[string]bool{})
			}
		}
	}
}
