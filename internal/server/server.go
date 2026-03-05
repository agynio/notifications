package server

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
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
	Subscribe() (<-chan *notificationsv1.NotificationEnvelope, func())
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
	clock       Clock
	idGenerator IDGenerator
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

// Subscribe streams live notifications to the caller until the context is
// cancelled or the subscription is otherwise terminated.
func (s *Server) Subscribe(req *notificationsv1.SubscribeRequest, stream notificationsv1.NotificationsService_SubscribeServer) error {
	ch, cancel := s.hub.Subscribe()
	defer cancel()

	ctx := stream.Context()
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
