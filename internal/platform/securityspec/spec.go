// Package securityspec reads the security-test contract from the actual OpenAPI specification.
package securityspec

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Operation struct {
	ID     string `json:"operation_id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Scope  string `json:"scope"`
}

func Load(repositoryRoot string) ([]Operation, error) {
	root, e := os.OpenRoot(repositoryRoot)
	if e != nil {
		return nil, e
	}
	defer func() { _ = root.Close() }()
	data, e := root.ReadFile("api/openapi.yaml")
	if e != nil {
		return nil, e
	}
	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if e = yaml.Unmarshal(data, &spec); e != nil {
		return nil, e
	}
	raw, e := root.ReadFile("api/security-coverage.json")
	if e != nil {
		return nil, e
	}
	var registry []Operation
	if e = json.Unmarshal(raw, &registry); e != nil {
		return nil, e
	}
	byID := map[string]Operation{}
	for _, op := range registry {
		if _, ok := byID[op.ID]; ok {
			return nil, errors.New("duplicate security case")
		}
		byID[op.ID] = op
	}
	out := []Operation{}
	for path, item := range spec.Paths {
		for method, node := range item {
			switch method {
			case "get", "post", "put", "patch", "delete", "head", "options", "trace":
			default:
				continue
			}
			var value struct {
				ID string `yaml:"operationId"`
			}
			if e = node.Decode(&value); e != nil {
				return nil, e
			}
			if value.ID == "" {
				return nil, errors.New("OpenAPI operation missing operationId: " + method + " " + path)
			}
			op, ok := byID[value.ID]
			if !ok || op.Method != strings.ToUpper(method) || op.Path != path {
				return nil, errors.New("OpenAPI operation lacks an exact security case: " + value.ID)
			}
			switch op.Scope {
			case "public", "identity", "collection", "resource", "websocket", "oauth", "webhook":
			default:
				return nil, errors.New("invalid security scope")
			}
			out = append(out, op)
			delete(byID, value.ID)
		}
	}
	if len(byID) > 0 {
		return nil, errors.New("stale security cases")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
