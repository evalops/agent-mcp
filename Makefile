SERVICE=agent-mcp

.PHONY: build test run fmt

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/$(SERVICE)

fmt:
	gofmt -w ./cmd ./internal
