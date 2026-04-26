package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
)

const (
	identityMetadata                  = "x-identity-id"
	organizationMemberRelation        = "member"
	organizationViewWorkloadsRelation = "can_view_workloads"
	identityObjectPrefix              = "identity:"
	organizationObjectPrefix          = "organization:"
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
	for _, room := range rooms {
		switch room.kind {
		case roomKindThreadParticipant:
			if room.id != identityID {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindWorkload:
			organizationID, err := s.workloadOrganizationID(ctx, room.id)
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
		case roomKindTrace:
			if s.traceOrgResolver == nil {
				return status.Error(codes.Internal, "trace org resolver unavailable")
			}
			organizationID, ok := s.traceOrgResolver.OrgIDForTrace(room.traceID)
			if !ok {
				return status.Error(codes.PermissionDenied, "permission denied")
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
