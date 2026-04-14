SERVICE=agent-mcp

.PHONY: build test lint run fmt

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
