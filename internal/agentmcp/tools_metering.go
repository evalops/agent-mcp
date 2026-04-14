package agentmcp

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	meterv1 "github.com/evalops/proto/gen/go/meter/v1"
	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type reportUsageInput struct {
	Model        string  `json:"model,omitempty" jsonschema:"Model name used for inference"`
	Provider     string  `json:"provider,omitempty" jsonschema:"Provider name such as anthropic or openai"`
	InputTokens  int64   `json:"input_tokens,omitempty" jsonschema:"Number of input tokens consumed"`
	OutputTokens int64   `json:"output_tokens,omitempty" jsonschema:"Number of output tokens produced"`
	CostUSD      float64 `json:"cost_usd,omitempty" jsonschema:"Estimated cost in USD"`
	EventType    string  `json:"event_type,omitempty" jsonschema:"Event type such as inference or tool_call"`
}

type reportUsageOutput struct {
	Recorded bool `json:"recorded"`
}

func (rc *requestContext) toolReportUsage(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	input reportUsageInput,
) (*mcpsdk.CallToolResult, reportUsageOutput, error) {
	if rc.deps.Meter == nil || rc.deps.Config.Meter.BaseURL == "" {
		return nil, reportUsageOutput{Recorded: false}, nil
	}

	sid := rc.mcpSessionID()
	state, _ := rc.deps.Sessions.Get(sid)

	agentID := ""
	agentToken := ""
	surface := ""
	if state != nil {
		agentID = state.AgentID
		agentToken = state.AgentToken
		surface = state.Surface
	}

	eventType := input.EventType
	if eventType == "" {
		eventType = "inference"
	}

	req := connect.NewRequest(&meterv1.RecordUsageRequest{
		AgentId:      agentID,
		Surface:      surface,
		EventType:    eventType,
		Model:        input.Model,
		Provider:     input.Provider,
		InputTokens:  input.InputTokens,
		OutputTokens: input.OutputTokens,
		TotalCostUsd: input.CostUSD,
		RequestId:    uuid.New().String(),
	})
	if agentToken != "" {
		req.Header().Set("Authorization", "Bearer "+agentToken)
	}

	if rc.deps.Breakers != nil && !rc.deps.Breakers.Meter.Allow() {
		rc.logger.Warn("meter circuit breaker open -- skipping usage report (fail-open)", "agent_id", agentID, "model", input.Model)
		return nil, reportUsageOutput{Recorded: false}, nil
	}
	start := time.Now()
	if _, err := rc.deps.Meter.RecordUsage(ctx, req); err != nil {
		rc.deps.Metrics.DownstreamErrors.WithLabelValues("meter").Inc()
		if rc.deps.Breakers != nil {
			rc.deps.Breakers.Meter.RecordFailure()
		}
		rc.deps.Metrics.DownstreamLatency.WithLabelValues("meter", "record_usage").Observe(time.Since(start).Seconds())
		rc.logger.Error("meter record usage failed", "error", err)
		return nil, reportUsageOutput{}, fmt.Errorf("meter record usage failed: %w", err)
	}
	if rc.deps.Breakers != nil {
		rc.deps.Breakers.Meter.RecordSuccess()
	}
	rc.deps.Metrics.DownstreamLatency.WithLabelValues("meter", "record_usage").Observe(time.Since(start).Seconds())

	rc.deps.Metrics.UsageReports.Inc()
	rc.logger.Info("usage reported", "agent_id", agentID, "model", input.Model, "input_tokens", input.InputTokens, "output_tokens", input.OutputTokens)

	return nil, reportUsageOutput{Recorded: true}, nil
}
