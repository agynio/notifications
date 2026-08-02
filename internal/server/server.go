package server

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

// Publisher defines the Redis publisher behaviour expected by the gRPC server.
type Publisher interface {
	Publish(ctx context.Context, envelope *notificationsv1.NotificationEnvelope) error
}

// SubscriptionHub captures the subset of stream.Hub behaviour needed by the
// server to register streaming clients.
type SubscriptionHub interface {
	Subscribe(rooms []string) (<-chan *notificationsv1.NotificationEnvelope, func())
}

// Clock produces the current time. Allows determinism in tests.
type Clock func() time.Time

// IDGenerator provides unique identifiers for envelopes.
type IDGenerator func() string

// Option mutates server configuration.
type Option func(*Server)

// WithClock overrides the clock used for timestamp generation.
func WithClock(clock Clock) Option {
	return func(s *Server) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// WithIDGenerator overrides the ID generator used for envelopes.
func WithIDGenerator(generator IDGenerator) Option {
	return func(s *Server) {
		if generator != nil {
			s.idGenerator = generator
		}
	}
}

// Server implements the NotificationsService gRPC handlers.
type Server struct {
	notificationsv1.UnimplementedNotificationsServiceServer

	logger      *zap.Logger
	publisher   Publisher
	hub         SubscriptionHub
	authz       Authorizer
	workloads   WorkloadReader
	agents      AgentReader
	clock       Clock
	idGenerator IDGenerator
}

// WithAuthorization supplies the clients Subscribe needs to decide who may
// listen to an entity's room: the Authorization service, plus the two services
// that own the entities whose rooms are keyed by something other than an
// organization.
func WithAuthorization(authz Authorizer, workloads WorkloadReader, agents AgentReader) Option {
	return func(s *Server) {
		s.authz = authz
		s.workloads = workloads
		s.agents = agents
	}
}

// New constructs a Server with the provided dependencies.
func New(publisher Publisher, hub SubscriptionHub, logger *zap.Logger, opts ...Option) *Server {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Server{
		logger:      logger,
		publisher:   publisher,
		hub:         hub,
		clock:       func() time.Time { return time.Now().UTC() },
		idGenerator: func() string { return uuid.NewString() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Publish validates the request, creates an envelope, and publishes it through
// the configured Publisher.
func (s *Server) Publish(ctx context.Context, req *notificationsv1.PublishRequest) (*notificationsv1.PublishResponse, error) {
	if err := validatePublishRequest(req); err != nil {
		return nil, err
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:      s.idGenerator(),
		Ts:      timestamppb.New(s.clock()),
		Source:  req.GetSource(),
		Event:   req.GetEvent(),
		Rooms:   cloneRooms(req.GetRooms()),
		Payload: req.GetPayload(),
	}

	if err := s.publisher.Publish(ctx, envelope); err != nil {
		s.logger.Error("publish failed", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "publish failed")
	}

	return &notificationsv1.PublishResponse{Id: envelope.Id, Ts: envelope.Ts}, nil
}

type callerIdentity struct {
	id           uuid.UUID
	identityType string
}

const agentInstanceIdentityType = "agent_instance"

func identityIDFromContext(ctx context.Context) (uuid.UUID, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return uuid.UUID{}, status.Error(codes.Unauthenticated, "identity not available")
	}
	for _, value := range md.Get("x-identity-id") {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		id, err := uuid.Parse(trimmed)
		if err != nil {
			return uuid.UUID{}, status.Errorf(codes.Unauthenticated, "invalid identity metadata: %v", err)
		}
		return id, nil
	}
	return uuid.UUID{}, status.Error(codes.Unauthenticated, "identity not available")
}

func identityTypeFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get("x-identity-type") {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func callerIdentityFromContext(ctx context.Context) (callerIdentity, error) {
	identityID, err := identityIDFromContext(ctx)
	if err != nil {
		return callerIdentity{}, err
	}
	return callerIdentity{id: identityID, identityType: identityTypeFromContext(ctx)}, nil
}

// authorizeSubscribeRooms applies the room access table in
// architecture/authz.md#notifications-service. Identity-keyed rooms are settled
// by equality; every other room is gated on the caller's relation to the
// organization that owns the entity the room reports on.
func (s *Server) authorizeSubscribeRooms(ctx context.Context, caller callerIdentity, rooms []subscriptionRoom) error {
	for _, room := range rooms {
		switch room.kind {
		case roomKindThreadParticipant:
			if room.id != caller.id {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindInstanceInbox:
			if room.id != caller.id || caller.identityType != agentInstanceIdentityType {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindSandboxOwner:
			// Only the owner, and ":me" is deliberately not accepted here.
			if room.id != caller.id {
				return status.Error(codes.PermissionDenied, "permission denied")
			}
		case roomKindOrganization:
			if err := s.requireOrganizationRelation(ctx, caller.id, room.id, memberRelation); err != nil {
				return err
			}
		case roomKindSandboxOrg:
			if err := s.requireOrganizationRelation(ctx, caller.id, room.id, canListSandboxesRelation); err != nil {
				return err
			}
		case roomKindWorkload:
			organizationID, err := s.workloadOrganization(ctx, room.id)
			if err != nil {
				return err
			}
			if err := s.requireOrganizationRelation(ctx, caller.id, organizationID, memberRelation); err != nil {
				return err
			}
		// Instance state -- created, paused, resumed, terminated. Gated like
		// the agent room it mirrors: member on the owning organization. That
		// also admits the instance itself, whose identity satisfies member
		// through its own org relation, which is how the Orchestrator watches
		// the instances it reconciles.
		case roomKindAgentInstance:
			organizationID, err := s.agentInstanceOrganization(ctx, room.id)
			if err != nil {
				return err
			}
			if err := s.requireOrganizationRelation(ctx, caller.id, organizationID, memberRelation); err != nil {
				return err
			}
		case roomKindAgent:
			organizationID, err := s.agentOrganization(ctx, room.id)
			if err != nil {
				return err
			}
			if err := s.requireOrganizationRelation(ctx, caller.id, organizationID, memberRelation); err != nil {
				return err
			}
		case roomKindEgressRules:
			// One global room, carrying rule invalidations to the Egress
			// Gateway. It is not organization-keyed the way authz.md describes
			// -- publisher and subscriber both use the bare literal -- so there
			// is no organization to check against. Named here so the default
			// below does not silently cut the gateway off.
		case roomKindTrace, roomKindVolume:
			// Both are specified as "member on the owning organization", and
			// neither owner will say which organization that is: GetTrace
			// returns raw OTLP spans, and neither the agents nor the runners
			// Volume message carries an organization id. Closing these needs
			// that field, so they stay open rather than break the console.
		default:
			// Unrecognised. Nothing publishes to it, so a subscription can only
			// be a probe, and a room kind added later has to state its policy
			// here rather than inherit an accidental allow.
			return status.Error(codes.PermissionDenied, "permission denied")
		}
	}
	return nil
}

// Subscribe streams live notifications to the caller until the context is
// cancelled or the subscription is otherwise terminated.
func (s *Server) Subscribe(req *notificationsv1.SubscribeRequest, stream notificationsv1.NotificationsService_SubscribeServer) error {
	ctx := stream.Context()
	caller, err := callerIdentityFromContext(ctx)
	if err != nil {
		return err
	}
	rooms, err := parseSubscribeRooms(req, caller.id)
	if err != nil {
		return err
	}
	if err := s.authorizeSubscribeRooms(ctx, caller, rooms); err != nil {
		return err
	}

	ch, cancel := s.hub.Subscribe(roomNames(rooms))
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case envelope, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(&notificationsv1.SubscribeResponse{Envelope: envelope}); err != nil {
				return err
			}
		}
	}
}

func validatePublishRequest(req *notificationsv1.PublishRequest) error {
	if req == nil {
		return status.Errorf(codes.InvalidArgument, "request required")
	}

	event := strings.TrimSpace(req.GetEvent())
	if event == "" {
		return status.Errorf(codes.InvalidArgument, "event required")
	}

	if len(req.GetRooms()) == 0 {
		return status.Errorf(codes.InvalidArgument, "at least one room required")
	}

	for i, room := range req.GetRooms() {
		if strings.TrimSpace(room) == "" {
			return status.Errorf(codes.InvalidArgument, "room %d is empty", i)
		}
	}

	if req.Payload == nil {
		return status.Errorf(codes.InvalidArgument, "payload required")
	}

	return nil
}

func cloneRooms(rooms []string) []string {
	if len(rooms) == 0 {
		return nil
	}
	cloned := make([]string, len(rooms))
	copy(cloned, rooms)
	return cloned
}
