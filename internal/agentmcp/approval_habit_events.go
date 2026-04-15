package agentmcp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	approvalsv1 "github.com/evalops/proto/gen/go/approvals/v1"
	"github.com/evalops/service-runtime/natsbus"
	"github.com/nats-io/nats.go"
)

func StartApprovalHabitSubscriber(ctx context.Context, natsURL, stream, subject, durable string, cache *ApprovalHabitsCache, logger *slog.Logger) (func(context.Context) error, error) {
	natsURL = strings.TrimSpace(natsURL)
	stream = strings.TrimSpace(stream)
	subject = strings.TrimSpace(subject)
	durable = strings.TrimSpace(durable)
	if natsURL == "" || stream == "" || subject == "" || durable == "" || cache == nil {
		return func(context.Context) error { return nil }, nil
	}

	logger = loggerOrDefault(logger)
	return natsbus.Subscribe(ctx, natsURL, natsbus.ConsumerOptions{
		Stream:        stream,
		Subject:       subject,
		Durable:       durable,
		Queue:         durable,
		DeliverPolicy: natsbus.DeliverNew,
		Logger:        logger,
	}, func(_ context.Context, message *nats.Msg) error {
		updated, err := ingestApprovalHabitMessage(cache, message)
		if err != nil {
			if shouldSkipApprovalHabitMessage(err) {
				logger.Debug("approval habit cache update skipped", "error", err)
				return nil
			}
			return err
		}
		if updated {
			logger.Debug("approval habit cache updated from event")
		}
		return nil
	})
}

func ingestApprovalHabitMessage(cache *ApprovalHabitsCache, message *nats.Msg) (bool, error) {
	if cache == nil {
		return false, fmt.Errorf("skip_cache_nil")
	}
	envelope, err := natsbus.UnmarshalMessage(message)
	if err != nil {
		return false, fmt.Errorf("skip_unmarshal_envelope: %w", err)
	}
	habit := &approvalsv1.ApprovalHabit{}
	if err := natsbus.UnmarshalPayload(envelope.Payload, habit); err != nil {
		return false, fmt.Errorf("skip_unmarshal_payload: %w", err)
	}
	workspaceID := strings.TrimSpace(envelope.TenantID)
	if workspaceID == "" {
		return false, fmt.Errorf("skip_missing_workspace_id")
	}
	if strings.TrimSpace(habit.GetPattern()) == "" {
		return false, fmt.Errorf("skip_missing_pattern")
	}
	return cache.Upsert(workspaceID, habit), nil
}

func shouldSkipApprovalHabitMessage(err error) bool {
	message := err.Error()
	return strings.HasPrefix(message, "skip_")
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
