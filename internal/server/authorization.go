package server

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/agents/v1"
	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
)

// Authorizer is the subset of the Authorization service this server calls.
type Authorizer interface {
	Check(ctx context.Context, req *authorizationv1.CheckRequest, opts ...grpc.CallOption) (*authorizationv1.CheckResponse, error)
}

// WorkloadReader resolves the organization a workload belongs to. Workload
// rooms are keyed by workload id, and the room's access check is stated against
// that workload's organization.
type WorkloadReader interface {
	GetWorkload(ctx context.Context, req *runnersv1.GetWorkloadRequest, opts ...grpc.CallOption) (*runnersv1.GetWorkloadResponse, error)
}

// AgentReader resolves the organization an agent belongs to, for the same
// reason as WorkloadReader.
type AgentReader interface {
	GetAgent(ctx context.Context, req *agentsv1.GetAgentRequest, opts ...grpc.CallOption) (*agentsv1.GetAgentResponse, error)
	GetInstance(ctx context.Context, req *agentsv1.GetInstanceRequest, opts ...grpc.CallOption) (*agentsv1.GetInstanceResponse, error)
}

const (
	identityObjectPrefix     = "identity:"
	organizationObjectPrefix = "organization:"

	memberRelation           = "member"
	canListSandboxesRelation = "can_list_sandboxes"
)

// requireOrganizationRelation resolves the caller's relation to an
// organization. A failed lookup denies: a subscription outlives the request
// that opened it, so anything short of a definite yes has to refuse.
func (s *Server) requireOrganizationRelation(ctx context.Context, caller uuid.UUID, organizationID uuid.UUID, relation string) error {
	if s.authz == nil {
		return status.Error(codes.Internal, "authorization is not configured")
	}
	response, err := s.authz.Check(ctx, &authorizationv1.CheckRequest{
		TupleKey: &authorizationv1.TupleKey{
			User:     identityObjectPrefix + caller.String(),
			Relation: relation,
			Object:   organizationObjectPrefix + organizationID.String(),
		},
	})
	if err != nil {
		s.logger.Warn("subscribe authorization check failed")
		return status.Error(codes.PermissionDenied, "permission denied")
	}
	if !response.GetAllowed() {
		return status.Error(codes.PermissionDenied, "permission denied")
	}
	return nil
}

// workloadOrganization reads the organization owning a workload. Notifications
// forwards no identity here: it is an internal caller, gated by the mesh
// policy, and the workload's organization is what the caller's access is then
// checked against.
func (s *Server) workloadOrganization(ctx context.Context, workloadID uuid.UUID) (uuid.UUID, error) {
	if s.workloads == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "runners client is not configured")
	}
	response, err := s.workloads.GetWorkload(ctx, &runnersv1.GetWorkloadRequest{Id: workloadID.String()})
	if err != nil {
		s.logger.Warn("subscribe workload lookup failed")
		return uuid.UUID{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return parseOrganization(response.GetWorkload().GetOrganizationId())
}

// agentOrganization reads the organization owning an agent, for the same reason
// as workloadOrganization.
func (s *Server) agentOrganization(ctx context.Context, agentID uuid.UUID) (uuid.UUID, error) {
	if s.agents == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "agents client is not configured")
	}
	response, err := s.agents.GetAgent(ctx, &agentsv1.GetAgentRequest{Id: agentID.String()})
	if err != nil {
		s.logger.Warn("subscribe agent lookup failed")
		return uuid.UUID{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return parseOrganization(response.GetAgent().GetOrganizationId())
}

func (s *Server) agentInstanceOrganization(ctx context.Context, agentInstanceID uuid.UUID) (uuid.UUID, error) {
	if s.agents == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "agents client is not configured")
	}
	response, err := s.agents.GetInstance(ctx, &agentsv1.GetInstanceRequest{Id: agentInstanceID.String()})
	if err != nil {
		s.logger.Warn("subscribe agent instance lookup failed")
		return uuid.UUID{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return parseOrganization(response.GetInstance().GetOrganizationId())
}

func parseOrganization(value string) (uuid.UUID, error) {
	id, err := parseUUID(value)
	if err != nil {
		return uuid.UUID{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return id, nil
}
