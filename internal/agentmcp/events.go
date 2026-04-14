package agentmcp

// EventPublisher abstracts lifecycle event publishing for testability.
type EventPublisher interface {
	Publish(tenantID, aggregateType, aggregateID, operation string, attrs map[string]any)
	Close()
}

// NoopEventPublisher discards all events. Used when NATS is not configured.
type NoopEventPublisher struct{}

func (NoopEventPublisher) Publish(_, _, _, _ string, _ map[string]any) {}
func (NoopEventPublisher) Close()                                      {}
