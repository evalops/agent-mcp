package agentmcp

import (
	"context"
	"testing"
	"time"

	eventsv1 "github.com/evalops/proto/gen/go/events/v1"
	"github.com/evalops/service-runtime/natsbus"
)

type recordedEvent struct {
	tenantID      string
	aggregateType string
	aggregateID   string
	operation     string
	attrs         map[string]any
}

type recordingEventPublisher struct {
	events []recordedEvent
}

func (p *recordingEventPublisher) Publish(_ context.Context, tenantID, aggregateType, aggregateID, operation string, attrs map[string]any) {
	cloned := make(map[string]any, len(attrs))
	for key, value := range attrs {
		cloned[key] = value
	}
	p.events = append(p.events, recordedEvent{
		tenantID:      tenantID,
		aggregateType: aggregateType,
		aggregateID:   aggregateID,
		operation:     operation,
		attrs:         cloned,
	})
}

func (*recordingEventPublisher) Close() {}

type recordingChangePublisher struct {
	changes []natsbus.Change
}

func (p *recordingChangePublisher) PublishChange(_ context.Context, change natsbus.Change) {
	p.changes = append(p.changes, change)
}

func TestNATSEventPublisherPublishesChangeEnvelope(t *testing.T) {
	bus := &recordingChangePublisher{}
	publisher := NewNATSEventPublisher(bus, testLogger, nil)
	recordedAt := time.Date(2026, 4, 14, 16, 45, 0, 0, time.UTC)
	publisher.now = func() time.Time { return recordedAt }

	publisher.Publish(context.Background(), "ws_123", "governance_check", "agent_1", "evaluated", map[string]any{
		"action_type":       "Bash",
		"agent_id":          "agent_1",
		"approval_required": true,
		"matched_rules":     []string{"dangerous-bash"},
		"reasons":           []string{"destructive command"},
		"risk_level":        "critical",
	})

	if len(bus.changes) != 1 {
		t.Fatalf("expected 1 published change, got %d", len(bus.changes))
	}

	change := bus.changes[0]
	if change.TenantID != "ws_123" {
		t.Fatalf("expected tenant ws_123, got %q", change.TenantID)
	}
	if change.AggregateType != "governance_check" {
		t.Fatalf("expected governance_check aggregate, got %q", change.AggregateType)
	}
	if change.AggregateID != "agent_1" {
		t.Fatalf("expected aggregate id agent_1, got %q", change.AggregateID)
	}
	if change.Operation != "evaluated" {
		t.Fatalf("expected evaluated operation, got %q", change.Operation)
	}
	if !change.RecordedAt.Equal(recordedAt) {
		t.Fatalf("expected recorded_at %s, got %s", recordedAt, change.RecordedAt)
	}

	var payload eventsv1.Change
	if err := natsbus.UnmarshalPayload(change.Payload, &payload); err != nil {
		t.Fatalf("unmarshal nats payload: %v", err)
	}
	if payload.GetOrganizationId() != "ws_123" {
		t.Fatalf("expected payload org ws_123, got %q", payload.GetOrganizationId())
	}
	if payload.GetActorType() != "agent" {
		t.Fatalf("expected actor type agent, got %q", payload.GetActorType())
	}
	if payload.GetActorId() != "agent_1" {
		t.Fatalf("expected actor id agent_1, got %q", payload.GetActorId())
	}
	payloadMap := payload.GetPayload().AsMap()
	if payloadMap["action_type"] != "Bash" {
		t.Fatalf("expected action_type Bash, got %#v", payloadMap["action_type"])
	}
	if payloadMap["risk_level"] != "critical" {
		t.Fatalf("expected risk_level critical, got %#v", payloadMap["risk_level"])
	}
}

func TestNATSEventPublisherClose(t *testing.T) {
	closed := 0
	publisher := NewNATSEventPublisher(&recordingChangePublisher{}, testLogger, func() { closed++ })
	publisher.Close()
	if closed != 1 {
		t.Fatalf("expected close to run once, got %d", closed)
	}
}
