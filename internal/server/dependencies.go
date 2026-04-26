package server

import (
	"context"

	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
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
