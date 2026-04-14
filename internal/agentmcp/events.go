package agentmcp

import (
	"context"
	"log/slog"
	"time"

	eventsv1 "github.com/evalops/proto/gen/go/events/v1"
	"github.com/evalops/service-runtime/natsbus"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventPublisher abstracts lifecycle event publishing for testability.
type EventPublisher interface {
	Publish(ctx context.Context, tenantID, aggregateType, aggregateID, operation string, attrs map[string]any)
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

func (p *LogEventPublisher) Publish(_ context.Context, tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	p.logger.Info("lifecycle event",
		"tenant_id", tenantID,
		"aggregate_type", aggregateType,
		"aggregate_id", aggregateID,
		"operation", operation,
		"attrs", attrs,
	)
}

func (p *LogEventPublisher) Close() {}

// NATSEventPublisher publishes event envelopes to the shared JetStream bus.
type NATSEventPublisher struct {
	publisher natsbus.ChangePublisher
	logger    *slog.Logger
	closeFunc func()
	now       func() time.Time
}

func NewNATSEventPublisher(publisher natsbus.ChangePublisher, logger *slog.Logger, closeFunc func()) *NATSEventPublisher {
	return &NATSEventPublisher{
		publisher: publisher,
		logger:    logger,
		closeFunc: closeFunc,
		now:       time.Now,
	}
}

func (p *NATSEventPublisher) Publish(ctx context.Context, tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	if p == nil || p.publisher == nil {
		return
	}

	payload, err := eventPayload(attrs)
	if err != nil {
		p.loggerOrDefault().Error("failed to build event payload", "error", err, "aggregate_type", aggregateType, "operation", operation)
		return
	}

	recordedAt := p.now().UTC()
	changePayload := &eventsv1.Change{
		OrganizationId: tenantID,
		AggregateType:  aggregateType,
		AggregateId:    aggregateID,
		Operation:      operation,
		ActorType:      eventActorType(attrs),
		ActorId:        eventActorID(aggregateID, attrs),
		RecordedAt:     timestamppb.New(recordedAt),
		Payload:        payload,
	}

	p.publisher.PublishChange(ctx, natsbus.Change{
		TenantID:      tenantID,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Operation:     operation,
		RecordedAt:    recordedAt,
		Payload:       natsbus.MustPayload(changePayload),
	})
}

func (p *NATSEventPublisher) Close() {
	if p != nil && p.closeFunc != nil {
		p.closeFunc()
	}
}

// NoopEventPublisher discards all events. Used when NATS is not configured.
type NoopEventPublisher struct{}

func (NoopEventPublisher) Publish(context.Context, string, string, string, string, map[string]any) {}
func (NoopEventPublisher) Close()                                                                  {}

func eventPayload(attrs map[string]any) (*structpb.Struct, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	normalized := make(map[string]any, len(attrs))
	for key, value := range attrs {
		normalized[key] = normalizeEventValue(value)
	}
	return structpb.NewStruct(normalized)
}

func eventActorID(aggregateID string, attrs map[string]any) string {
	if value, ok := attrs["actor_id"].(string); ok && value != "" {
		return value
	}
	if value, ok := attrs["agent_id"].(string); ok && value != "" {
		return value
	}
	return aggregateID
}

func eventActorType(attrs map[string]any) string {
	if value, ok := attrs["actor_type"].(string); ok && value != "" {
		return value
	}
	if actorID := eventActorID("", attrs); actorID != "" {
		return "agent"
	}
	return "system"
}

func (p *NATSEventPublisher) loggerOrDefault() *slog.Logger {
	if p != nil && p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

func normalizeEventValue(value any) any {
	switch typed := value.(type) {
	case []string:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}
		return values
	case []any:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, normalizeEventValue(item))
		}
		return values
	case map[string]string:
		values := make(map[string]any, len(typed))
		for key, item := range typed {
			values[key] = item
		}
		return values
	case map[string]any:
		values := make(map[string]any, len(typed))
		for key, item := range typed {
			values[key] = normalizeEventValue(item)
		}
		return values
	default:
		return value
	}
}
