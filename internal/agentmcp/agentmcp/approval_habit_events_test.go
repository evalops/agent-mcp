package agentmcp

import (
	"fmt"
	"testing"
	"time"

	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestIngestApprovalHabitMessageUpdatesCachedWorkspace(t *testing.T) {
	cache := NewApprovalHabitsCache()
	cache.Store("ws_1", []*approvalsv1.ApprovalHabit{{
		Pattern:               "chat:crm_update",
		ObservationCount:      5,
		ApprovedCount:         4,
		AutoApproveConfidence: 0.8,
	}})

	updated, err := ingestApprovalHabitMessage(cache, mustApprovalHabitMessage(t, "ws_1", "approvals.events.approval_habit.habit-learned", time.Date(2026, 4, 15, 8, 0, 0, 0, time.UTC), &approvalsv1.ApprovalHabit{
		Pattern:               "chat:crm_update",
		ObservationCount:      6,
		ApprovedCount:         5,
		AutoApproveConfidence: 0.8333,
	}))
	if err != nil {
		t.Fatalf("ingestApprovalHabitMessage() error = %v", err)
	}
	if !updated {
		t.Fatal("expected cached workspace to be updated")
	}
	habits, ok := cache.Get("ws_1")
	if !ok || len(habits) != 1 {
		t.Fatalf("expected one cached habit, got %#v", habits)
	}
	if habits[0].GetApprovedCount() != 5 {
		t.Fatalf("expected approved_count 5, got %d", habits[0].GetApprovedCount())
	}
}

func TestIngestApprovalHabitMessageSkipsUncachedWorkspace(t *testing.T) {
	cache := NewApprovalHabitsCache()

	updated, err := ingestApprovalHabitMessage(cache, mustApprovalHabitMessage(t, "ws_2", "approvals.events.approval_habit.habit-learned", time.Now().UTC(), &approvalsv1.ApprovalHabit{
		Pattern: "chat:crm_update",
	}))
	if err != nil {
		t.Fatalf("ingestApprovalHabitMessage() error = %v", err)
	}
	if updated {
		t.Fatal("expected uncached workspace to be ignored")
	}
}

func mustApprovalHabitMessage(t *testing.T, tenantID string, eventType string, recordedAt time.Time, habit *approvalsv1.ApprovalHabit) *nats.Msg {
	t.Helper()
	payload, err := anypb.New(habit)
	if err != nil {
		t.Fatalf("wrap habit payload: %v", err)
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal any payload: %v", err)
	}
	return &nats.Msg{Data: []byte(fmt.Sprintf(`{"specversion":"1.0","id":"evt_1","type":%q,"source":"approvals.events","time":%q,"datacontenttype":"application/json","tenant_id":%q,"data":%s}`, eventType, recordedAt.UTC().Format(time.RFC3339), tenantID, data))}
}
