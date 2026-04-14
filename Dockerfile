FROM golang:1.26.2 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/agent-mcp ./cmd/agent-mcp

FROM gcr.io/distroless/static-debian12

COPY --from=build /out/agent-mcp /usr/local/bin/agent-mcp

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/agent-mcp"]
