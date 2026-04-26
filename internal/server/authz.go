package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/agents/v1"
	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
	tracingv1 "github.com/agynio/notifications/internal/.gen/agynio/api/tracing/v1"
	otlpv1 "github.com/agynio/notifications/internal/.gen/opentelemetry/proto/trace/v1"
)

const (
	identityMetadata                  = "x-identity-id"
	organizationMemberRelation        = "member"
	organizationViewWorkloadsRelation = "can_view_workloads"
	identityObjectPrefix              = "identity:"
	organizationObjectPrefix          = "organization:"
	traceOrganizationIDAttribute      = "agyn.organization.id"
)

func identityFromMetadata(ctx context.Context) (uuid.UUID, bool, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return uuid.UUID{}, false, nil
	}
	values := md.Get(identityMetadata)
	if len(values) == 0 {
		return uuid.UUID{}, false, nil
	}
	if len(values) != 1 {
		return uuid.UUID{}, true, fmt.Errorf("expected single %s", identityMetadata)
	}
	identityID, err := parseUUID(values[0])
	if err != nil {
		return uuid.UUID{}, true, err
	}
	return identityID, true, nil
}

func (s *Server) authorizeSubscribe(ctx context.Context, identityID uuid.UUID, rooms []subscriptionRoom) error {
	memberCache := map[uuid.UUID]bool{}
	viewWorkloadsCache := map[uuid.UUID]bool{}
	downstreamCtx := withIdentityMetadata(ctx, identityID)
	for _, room := range rooms {
		switch room.kind {
		case roomKindThreadParticipant:
			if room.id != identityID {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindWorkload:
			organizationID, err := s.workloadOrganizationID(downstreamCtx, room.id)
			if err != nil {
				return err
			}
			allowed, err := s.viewWorkloadsAllowed(ctx, identityID, organizationID, viewWorkloadsCache)
			if err != nil {
				return err
			}
			if !allowed {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindOrganization:
			allowed, err := s.viewWorkloadsAllowed(ctx, identityID, room.id, viewWorkloadsCache)
			if err != nil {
				return err
			}
			if !allowed {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindAgent:
			organizationID, err := s.agentOrganizationID(downstreamCtx, room.id)
			if err != nil {
				return err
			}
			allowed, err := s.memberAllowed(ctx, identityID, organizationID, memberCache)
			if err != nil {
				return err
			}
			if !allowed {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindTrace:
			organizationID, err := s.traceOrganizationID(downstreamCtx, room.traceID)
			if err != nil {
				return err
			}
			allowed, err := s.memberAllowed(ctx, identityID, organizationID, memberCache)
			if err != nil {
				return err
			}
			if !allowed {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindOther:
			return status.Error(codes.PermissionDenied, "permission denied")
		}
	}
	return nil
}

func (s *Server) workloadOrganizationID(ctx context.Context, workloadID uuid.UUID) (uuid.UUID, error) {
	if s.workloadOrgResolver != nil {
		if organizationID, ok := s.workloadOrgResolver.OrgIDForWorkload(workloadID); ok {
			return organizationID, nil
		}
	}
	if s.runnersClient == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "runners client unavailable")
	}
	response, err := s.runnersClient.GetWorkload(ctx, &runnersv1.GetWorkloadRequest{Id: workloadID.String()})
	if err != nil {
		if statusErr, ok := status.FromError(err); ok {
			return uuid.UUID{}, status.Error(statusErr.Code(), statusErr.Message())
		}
		return uuid.UUID{}, status.Errorf(codes.Internal, "get workload: %v", err)
	}
	workload := response.GetWorkload()
	if workload == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "workload not found")
	}
	orgID := strings.TrimSpace(workload.GetOrganizationId())
	if orgID == "" {
		return uuid.UUID{}, status.Error(codes.Internal, "workload missing organization_id")
	}
	organizationID, err := uuid.Parse(orgID)
	if err != nil {
		return uuid.UUID{}, status.Errorf(codes.Internal, "workload organization_id invalid: %v", err)
	}
	return organizationID, nil
}

func (s *Server) agentOrganizationID(ctx context.Context, agentID uuid.UUID) (uuid.UUID, error) {
	if s.agentsClient == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "agents client unavailable")
	}
	response, err := s.agentsClient.GetAgent(ctx, &agentsv1.GetAgentRequest{Id: agentID.String()})
	if err != nil {
		if statusErr, ok := status.FromError(err); ok {
			return uuid.UUID{}, status.Error(statusErr.Code(), statusErr.Message())
		}
		return uuid.UUID{}, status.Errorf(codes.Internal, "get agent: %v", err)
	}
	agent := response.GetAgent()
	if agent == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "agent not found")
	}
	orgID := strings.TrimSpace(agent.GetOrganizationId())
	if orgID == "" {
		return uuid.UUID{}, status.Error(codes.Internal, "agent missing organization_id")
	}
	organizationID, err := uuid.Parse(orgID)
	if err != nil {
		return uuid.UUID{}, status.Errorf(codes.Internal, "agent organization_id invalid: %v", err)
	}
	return organizationID, nil
}

func (s *Server) traceOrganizationID(ctx context.Context, traceID string) (uuid.UUID, error) {
	if s.traceOrgResolver != nil {
		if organizationID, ok := s.traceOrgResolver.OrgIDForTrace(traceID); ok {
			return organizationID, nil
		}
		if s.tracingClient == nil {
			return uuid.UUID{}, status.Error(codes.PermissionDenied, "permission denied")
		}
	} else if s.tracingClient == nil {
		return uuid.UUID{}, status.Error(codes.Internal, "trace org resolver unavailable")
	}

	traceBytes, err := hex.DecodeString(traceID)
	if err != nil || len(traceBytes) != 16 {
		return uuid.UUID{}, status.Error(codes.Internal, "trace id invalid")
	}
	response, err := s.tracingClient.GetTrace(ctx, &tracingv1.GetTraceRequest{TraceId: traceBytes})
	if err != nil {
		if statusErr, ok := status.FromError(err); ok {
			return uuid.UUID{}, status.Error(statusErr.Code(), statusErr.Message())
		}
		return uuid.UUID{}, status.Errorf(codes.Internal, "get trace: %v", err)
	}
	organizationID, err := traceOrganizationFromResourceSpans(response.GetResourceSpans())
	if err != nil {
		return uuid.UUID{}, err
	}
	return organizationID, nil
}

func traceOrganizationFromResourceSpans(resourceSpans []*otlpv1.ResourceSpans) (uuid.UUID, error) {
	for _, spans := range resourceSpans {
		resource := spans.GetResource()
		if resource == nil {
			continue
		}
		for _, attr := range resource.GetAttributes() {
			if attr.GetKey() != traceOrganizationIDAttribute {
				continue
			}
			value := strings.TrimSpace(attr.GetValue().GetStringValue())
			if value == "" {
				return uuid.UUID{}, status.Error(codes.Internal, "trace organization_id invalid")
			}
			organizationID, err := uuid.Parse(value)
			if err != nil {
				return uuid.UUID{}, status.Errorf(codes.Internal, "trace organization_id invalid: %v", err)
			}
			return organizationID, nil
		}
	}

	return uuid.UUID{}, status.Error(codes.Internal, "trace missing organization_id")
}

func (s *Server) memberAllowed(ctx context.Context, identityID uuid.UUID, organizationID uuid.UUID, cache map[uuid.UUID]bool) (bool, error) {
	if allowed, ok := cache[organizationID]; ok {
		return allowed, nil
	}
	allowed, err := s.relationAllowed(ctx, identityID, organizationMemberRelation, organizationObject(organizationID))
	if err != nil {
		return false, err
	}
	cache[organizationID] = allowed
	return allowed, nil
}

func (s *Server) viewWorkloadsAllowed(ctx context.Context, identityID uuid.UUID, organizationID uuid.UUID, cache map[uuid.UUID]bool) (bool, error) {
	if allowed, ok := cache[organizationID]; ok {
		return allowed, nil
	}
	allowed, err := s.relationAllowed(ctx, identityID, organizationViewWorkloadsRelation, organizationObject(organizationID))
	if err != nil {
		return false, err
	}
	cache[organizationID] = allowed
	return allowed, nil
}

func (s *Server) relationAllowed(ctx context.Context, identityID uuid.UUID, relation string, object string) (bool, error) {
	if s.authorizationClient == nil {
		return false, status.Error(codes.Internal, "authorization client unavailable")
	}
	resp, err := s.authorizationClient.Check(ctx, &authorizationv1.CheckRequest{
		TupleKey: &authorizationv1.TupleKey{
			User:     identityObject(identityID),
			Relation: relation,
			Object:   object,
		},
	})
	if err != nil {
		return false, status.Errorf(codes.Internal, "authorization check: %v", err)
	}
	return resp.GetAllowed(), nil
}

func identityObject(identityID uuid.UUID) string {
	return identityObjectPrefix + identityID.String()
}

func organizationObject(organizationID uuid.UUID) string {
	return organizationObjectPrefix + organizationID.String()
}

func withIdentityMetadata(ctx context.Context, identityID uuid.UUID) context.Context {
	return metadata.AppendToOutgoingContext(ctx, identityMetadata, identityID.String())
}
