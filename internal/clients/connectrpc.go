package clients

import (
	"net/http"

	"connectrpc.com/connect"
	agentsv1connect "github.com/evalops/proto/gen/go/agents/v1/agentsv1connect"
	approvalsv1connect "github.com/evalops/proto/gen/go/approvals/v1/approvalsv1connect"
	governancev1connect "github.com/evalops/proto/gen/go/governance/v1/governancev1connect"
	memoryv1connect "github.com/evalops/proto/gen/go/memory/v1/memoryv1connect"
	meterv1connect "github.com/evalops/proto/gen/go/meter/v1/meterv1connect"
)

// NewRegistryClient creates a ConnectRPC client for the agents/v1 AgentService.
func NewRegistryClient(baseURL string, httpClient *http.Client) agentsv1connect.AgentServiceClient {
	return agentsv1connect.NewAgentServiceClient(httpClient, baseURL, connect.WithGRPC())
}

// NewGovernanceClient creates a ConnectRPC client for governance/v1.
func NewGovernanceClient(baseURL string, httpClient *http.Client) governancev1connect.GovernanceServiceClient {
	return governancev1connect.NewGovernanceServiceClient(httpClient, baseURL, connect.WithGRPC())
}

// NewApprovalsClient creates a ConnectRPC client for approvals/v1.
func NewApprovalsClient(baseURL string, httpClient *http.Client) approvalsv1connect.ApprovalServiceClient {
	return approvalsv1connect.NewApprovalServiceClient(httpClient, baseURL, connect.WithGRPC())
}

// NewMeterClient creates a ConnectRPC client for meter/v1.
func NewMeterClient(baseURL string, httpClient *http.Client) meterv1connect.MeterServiceClient {
	return meterv1connect.NewMeterServiceClient(httpClient, baseURL, connect.WithGRPC())
}

// NewMemoryClient creates a ConnectRPC client for memory/v1.
func NewMemoryClient(baseURL string, httpClient *http.Client) memoryv1connect.MemoryServiceClient {
	return memoryv1connect.NewMemoryServiceClient(httpClient, baseURL, connect.WithGRPC())
}
