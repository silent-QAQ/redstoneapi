.PHONY: build generate test test-unit test-integration test-e2e test-redstone-migrations test-gateway-compat

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

test-redstone-migrations:
	go test -count=1 ./migrations
	go test -count=1 -tags=integration ./internal/repository -run '^(TestRedstoneMigrations_|TestRedstoneOwnedAccountsMigration_)'

test-gateway-compat:
	go test -count=1 ./internal/server/routes ./internal/service ./internal/pkg/apicompat ./internal/handler

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
