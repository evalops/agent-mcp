package agentmcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/evalops/service-runtime/natsbus"
)

// NATSEventPublisher publishes agent-mcp lifecycle events to NATS JetStream.
type NATSEventPublisher struct {
	publisher *natsbus.Publisher
	logger    *slog.Logger
}

func NewNATSEventPublisher(publisher *natsbus.Publisher, logger *slog.Logger) *NATSEventPublisher {
	return &NATSEventPublisher{publisher: publisher, logger: logger}
}

func (p *NATSEventPublisher) Publish(tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	p.publisher.PublishChange(context.Background(), natsbus.Change{
		TenantID:      tenantID,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Operation:     operation,
		RecordedAt:    time.Now().UTC(),
	})
}

func (p *NATSEventPublisher) Close() {
	p.publisher.Close()
}
