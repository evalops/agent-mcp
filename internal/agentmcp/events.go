package agentmcp

import (
	"log/slog"
)

// EventPublisher abstracts lifecycle event publishing for testability.
type EventPublisher interface {
	Publish(tenantID, aggregateType, aggregateID, operation string, attrs map[string]any)
	Close()
}

// LogEventPublisher logs events via slog. Used when NATS is configured,
// events are logged structurally for the audit trail.
type LogEventPublisher struct {
	logger *slog.Logger
}

func NewLogEventPublisher(logger *slog.Logger) *LogEventPublisher {
	return &LogEventPublisher{logger: logger}
}

func (p *LogEventPublisher) Publish(tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	p.logger.Info("lifecycle event",
		"tenant_id", tenantID,
		"aggregate_type", aggregateType,
		"aggregate_id", aggregateID,
		"operation", operation,
	)
}

func (p *LogEventPublisher) Close() {}

// NoopEventPublisher discards all events. Used when NATS is not configured.
type NoopEventPublisher struct{}

func (NoopEventPublisher) Publish(_, _, _, _ string, _ map[string]any) {}
func (NoopEventPublisher) Close()                                      {}
