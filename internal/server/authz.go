package server

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
)

const (
	identityMetadata           = "x-identity-id"
	organizationMemberRelation = "member"
	identityObjectPrefix       = "identity:"
	organizationObjectPrefix   = "organization:"
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
	for _, room := range rooms {
		switch room.kind {
		case roomKindThreadParticipant:
			if room.id != identityID {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindWorkload:
			if s.workloadOrgResolver == nil {
				return status.Error(codes.Internal, "workload org resolver unavailable")
			}
			organizationID, ok := s.workloadOrgResolver.OrgIDForWorkload(room.id)
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
		}
	}
	return nil
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
