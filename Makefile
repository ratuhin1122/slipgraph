GO_PACKAGES := ./cmd/... ./internal/...
# Override these with preinstalled binaries when using an offline tool cache.
GOLANGCI_LINT ?= go run -p=1 github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOSEC ?= go run -p=1 github.com/securego/gosec/v2/cmd/gosec@v2.30.0
GOVULNCHECK ?= go run -p=1 golang.org/x/vuln/cmd/govulncheck@v1.9.0

.PHONY: up down migrate test test-unit lint gen build
up:
	docker compose up --build -d --wait
down:
	docker compose down
migrate:
	docker compose run --rm migrate
test-unit:
	go test -race $(GO_PACKAGES)
	npm --prefix web test
test: test-unit
	go test -race -tags=integration -timeout=5m ./internal/platform/database
	go test -race -tags=integration -timeout=5m ./internal/auth
	go test ./internal/risk "-run=^$$" -bench=BenchmarkImpact -benchtime=3x
lint:
	go vet $(GO_PACKAGES)
	$(GOLANGCI_LINT) run $(GO_PACKAGES)
	$(GOSEC) -exclude-generated $(GO_PACKAGES)
	$(GOVULNCHECK) $(GO_PACKAGES)
	npm --prefix web run typecheck
	npm --prefix web run lint
	npm --prefix web audit --audit-level=low
gen:
	go run -p=1 github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
	go run -p=1 github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1 --config api/oapi-codegen.yaml api/openapi.yaml
	npm --prefix web run gen
build:
	go build $(GO_PACKAGES)
	npm --prefix web run build
