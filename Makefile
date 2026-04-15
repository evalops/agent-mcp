SERVICE=agent-mcp

.PHONY: build test lint run fmt install-hooks

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run ./...

run:
	go run ./cmd/$(SERVICE)

fmt:
	gofmt -w ./cmd ./internal

install-hooks:
	cp scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "Pre-commit hook installed"
