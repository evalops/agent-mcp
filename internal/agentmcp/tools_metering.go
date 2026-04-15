package agentmcp

import (
	"context"

	"connectrpc.com/connect"
	meterv1 "github.com/evalops/proto/gen/go/meter/v1"
	"github.com/evalops/service-runtime/downstream"
	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
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
	Recorded bool `json:"recorded"` // true means accepted for background recording, not confirmed persisted
	Async    bool `json:"async"`    // true when metering is fire-and-forget (normal operation)
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
	workspaceID := ""
	if state != nil {
		agentID = state.AgentID
		agentToken = state.AgentToken
		surface = state.Surface
		workspaceID = state.WorkspaceID
	}

	eventType := input.EventType
	if eventType == "" {
		eventType = "inference"
	}
	requestID := uuid.New().String()

	clonedMsg, _ := proto.Clone(&meterv1.RecordUsageRequest{
		AgentId:      agentID,
		Surface:      surface,
		EventType:    eventType,
		Model:        input.Model,
		Provider:     input.Provider,
		InputTokens:  input.InputTokens,
		OutputTokens: input.OutputTokens,
		TotalCostUsd: input.CostUSD,
		RequestId:    requestID,
	}).(*meterv1.RecordUsageRequest)

	eventAttrs := map[string]any{
		"agent_id":      agentID,
		"cost_usd":      input.CostUSD,
		"event_type":    eventType,
		"input_tokens":  input.InputTokens,
		"model":         input.Model,
		"output_tokens": input.OutputTokens,
		"provider":      input.Provider,
		"request_id":    requestID,
		"surface":       surface,
		"workspace_id":  workspaceID,
	}
	rc.logger.Info("usage reported", "agent_id", agentID, "model", input.Model, "input_tokens", input.InputTokens, "output_tokens", input.OutputTokens)

	go func() {
		// Detach from the request cancellation while still bounding the
		// downstream RPC lifetime.
		bgCtx, cancel := detachedContextWithTimeout(ctx, rc.deps.Config.Meter.RequestTimeout)
		defer cancel()

		bgReq := connect.NewRequest(clonedMsg)
		if agentToken != "" {
			bgReq.Header().Set("Authorization", "Bearer "+agentToken)
		}
		resp, _ := downstream.CallOp(bgCtx, rc.deps.downstreamClients().Meter, "record_usage", func(ctx context.Context) (*connect.Response[meterv1.RecordUsageResponse], error) {
			return rc.deps.Meter.RecordUsage(ctx, bgReq)
		})
		if resp != nil {
			rc.deps.Metrics.UsageReports.Inc()
			rc.deps.Events.Publish(bgCtx, workspaceID, "usage_report", requestID, "recorded", eventAttrs)
		}
	}()

	return nil, reportUsageOutput{Recorded: true, Async: true}, nil
}
