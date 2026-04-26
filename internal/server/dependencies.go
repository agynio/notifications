package server

import (
	"context"

	agentsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/agents/v1"
	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
	tracingv1 "github.com/agynio/notifications/internal/.gen/agynio/api/tracing/v1"
	"google.golang.org/grpc"
)

// AuthorizationClient provides access to permission checks.
type AuthorizationClient interface {
	Check(context.Context, *authorizationv1.CheckRequest, ...grpc.CallOption) (*authorizationv1.CheckResponse, error)
}

// RunnersClient provides access to workload lookups.
type RunnersClient interface {
	GetWorkload(context.Context, *runnersv1.GetWorkloadRequest, ...grpc.CallOption) (*runnersv1.GetWorkloadResponse, error)
}

// AgentsClient provides access to agent lookups.
type AgentsClient interface {
	GetAgent(context.Context, *agentsv1.GetAgentRequest, ...grpc.CallOption) (*agentsv1.GetAgentResponse, error)
}

// TracingClient provides access to trace lookups.
type TracingClient interface {
	GetTrace(context.Context, *tracingv1.GetTraceRequest, ...grpc.CallOption) (*tracingv1.GetTraceResponse, error)
}

// WithAuthorizationClient supplies the Authorization client for subscription auth.
func WithAuthorizationClient(client AuthorizationClient) Option {
	return func(s *Server) {
		s.authorizationClient = client
	}
}

// WithRunnersClient supplies the Runners client for workload lookups.
func WithRunnersClient(client RunnersClient) Option {
	return func(s *Server) {
		s.runnersClient = client
	}
}

// WithAgentsClient supplies the Agents client for agent lookups.
func WithAgentsClient(client AgentsClient) Option {
	return func(s *Server) {
		s.agentsClient = client
	}
}

// WithTracingClient supplies the Tracing client for trace lookups.
func WithTracingClient(client TracingClient) Option {
	return func(s *Server) {
		s.tracingClient = client
	}
}
