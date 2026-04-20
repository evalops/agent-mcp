package agentmcp

import (
	"log/slog"

	"github.com/evalops/service-runtime/downstream"
)

type DownstreamClients struct {
	Identity   *downstream.Client
	Registry   *downstream.Client
	Governance *downstream.Client
	Approvals  *downstream.Client
	Meter      *downstream.Client
	Memory     *downstream.Client
}

func (d *Deps) downstreamClients() *DownstreamClients {
	if d == nil {
		return nil
	}

	d.downstreamsOnce.Do(func() {
		d.downstreams = NewDownstreamClients(d.Breakers, d.Metrics, d.Logger)
	})
	return d.downstreams
}

func NewDownstreamClients(breakers *Breakers, metrics *Metrics, logger *slog.Logger) *DownstreamClients {
	return &DownstreamClients{
		Identity:   newDownstreamClient("identity", downstream.FailClosed, breakerFor("identity", breakers), metrics, logger),
		Registry:   newDownstreamClient("agent-registry", downstream.FailOpen, breakerFor("agent-registry", breakers), metrics, logger),
		Governance: newDownstreamClient("governance", downstream.FailClosed, breakerFor("governance", breakers), metrics, logger),
		Approvals:  newDownstreamClient("approvals", downstream.FailClosed, breakerFor("approvals", breakers), metrics, logger),
		Meter:      newDownstreamClient("meter", downstream.FailOpen, breakerFor("meter", breakers), metrics, logger),
		Memory:     newDownstreamClient("memory", downstream.FailOpen, breakerFor("memory", breakers), metrics, logger),
	}
}

func newDownstreamClient(name string, policy downstream.FailurePolicy, breaker *Breaker, metrics *Metrics, logger *slog.Logger) *downstream.Client {
	var downstreamMetrics *downstream.Metrics
	if metrics != nil {
		downstreamMetrics = &downstream.Metrics{
			Errors:  metrics.DownstreamErrors,
			Latency: metrics.DownstreamLatency,
		}
	}

	return downstream.New(name, policy, downstream.Config{
		Breaker: breaker,
		Logger:  logger,
		Metrics: downstreamMetrics,
	})
}

func breakerFor(name string, breakers *Breakers) *Breaker {
	if breakers == nil {
		return nil
	}

	switch name {
	case "identity":
		return breakers.Identity
	case "agent-registry":
		return breakers.Registry
	case "governance":
		return breakers.Governance
	case "approvals":
		return breakers.Approvals
	case "meter":
		return breakers.Meter
	case "memory":
		return breakers.Memory
	default:
		return nil
	}
}
