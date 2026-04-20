package agentmcp

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics for the agent-mcp service.
type Metrics struct {
	Registrations     *prometheus.CounterVec
	Heartbeats        prometheus.Counter
	Deregistrations   prometheus.Counter
	GovernanceChecks  *prometheus.CounterVec
	ApprovalRequests  *prometheus.CounterVec
	UsageReports      prometheus.Counter
	DownstreamErrors  *prometheus.CounterVec
	DownstreamLatency *prometheus.HistogramVec
	ActiveSessions    prometheus.Gauge
}

// NewMetrics creates and registers Prometheus metrics with the default registry.
func NewMetrics() *Metrics {
	return newMetricsWithRegistry(prometheus.DefaultRegisterer)
}

// NewTestMetrics creates metrics that don't register with the global registry.
// Safe for parallel tests.
func NewTestMetrics() *Metrics {
	return newMetricsWithRegistry(prometheus.NewRegistry())
}

func newMetricsWithRegistry(reg prometheus.Registerer) *Metrics {
	f := prometheus.WrapRegistererWithPrefix("", reg)

	registrations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_mcp_registrations_total",
		Help: "Total agent registrations",
	}, []string{"agent_type", "surface"})

	heartbeats := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "agent_mcp_heartbeats_total",
		Help: "Total agent heartbeats",
	})

	deregistrations := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "agent_mcp_deregistrations_total",
		Help: "Total agent deregistrations",
	})

	governanceChecks := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_mcp_governance_checks_total",
		Help: "Total governance action checks",
	}, []string{"decision", "risk_level"})

	approvalRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_mcp_approval_requests_total",
		Help: "Total approval requests created",
	}, []string{"risk_level"})

	usageReports := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "agent_mcp_usage_reports_total",
		Help: "Total usage reports sent to meter",
	})

	downstreamErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_mcp_downstream_errors_total",
		Help: "Errors from downstream service calls",
	}, []string{"downstream", "op"})

	downstreamLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agent_mcp_downstream_latency_seconds",
		Help:    "Latency of downstream service calls",
		Buckets: prometheus.DefBuckets,
	}, []string{"downstream", "op"})

	activeSessions := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "agent_mcp_active_sessions",
		Help: "Number of active MCP sessions",
	})

	// Register all — ignore errors for tests where metrics may already exist.
	for _, c := range []prometheus.Collector{
		registrations, heartbeats, deregistrations, governanceChecks,
		approvalRequests, usageReports, downstreamErrors, downstreamLatency, activeSessions,
	} {
		_ = f.Register(c)
	}

	return &Metrics{
		Registrations:     registrations,
		Heartbeats:        heartbeats,
		Deregistrations:   deregistrations,
		GovernanceChecks:  governanceChecks,
		ApprovalRequests:  approvalRequests,
		UsageReports:      usageReports,
		DownstreamErrors:  downstreamErrors,
		DownstreamLatency: downstreamLatency,
		ActiveSessions:    activeSessions,
	}
}
