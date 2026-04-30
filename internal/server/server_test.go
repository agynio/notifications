package server_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/agents/v1"
	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	runnersv1 "github.com/agynio/notifications/internal/.gen/agynio/api/runners/v1"
	"github.com/agynio/notifications/internal/server"
	"github.com/agynio/notifications/internal/stream"
)

const (
	bufSize             = 1024 * 1024
	identityMetadataKey = "x-identity-id"
)

func TestPublish(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2024, 9, 1, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		req      *notificationsv1.PublishRequest
		pubErr   error
		expectOK bool
		expectCd codes.Code
	}{
		"success": {
			req: &notificationsv1.PublishRequest{
				Event: "user.created",
				Rooms: []string{"room-a"},
				Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
					"foo": structpb.NewStringValue("bar"),
				}},
				Source: "api",
			},
			expectOK: true,
		},
		"publisher error": {
			req: &notificationsv1.PublishRequest{
				Event:   "user.created",
				Rooms:   []string{"room-a"},
				Payload: &structpb.Struct{Fields: map[string]*structpb.Value{"foo": structpb.NewStringValue("bar")}},
			},
			pubErr:   errors.New("redis down"),
			expectOK: false,
			expectCd: codes.Internal,
		},
		"validation error": {
			req:      &notificationsv1.PublishRequest{Rooms: []string{}, Payload: &structpb.Struct{}},
			expectOK: false,
			expectCd: codes.InvalidArgument,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stub := &publisherStub{err: tc.pubErr}
			hub := &noopHub{}
			client, cleanup := startTestServer(t, stub, hub, server.WithClock(func() time.Time { return fixedTime }), server.WithIDGenerator(func() string { return "fixed-id" }))
			defer cleanup()

			ctx := context.Background()
			resp, err := client.Publish(ctx, tc.req)
			if tc.expectOK {
				if err != nil {
					t.Fatalf("Publish returned error: %v", err)
				}
				if resp.GetId() != "fixed-id" {
					t.Fatalf("unexpected id: %s", resp.GetId())
				}
				if !resp.GetTs().AsTime().Equal(fixedTime) {
					t.Fatalf("unexpected timestamp: %v", resp.GetTs().AsTime())
				}
				if stub.envelope == nil {
					t.Fatal("expected envelope to be published")
				}
				if stub.envelope.GetId() != "fixed-id" {
					t.Fatalf("unexpected envelope id: %s", stub.envelope.GetId())
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected status error, got %v", err)
				}
				if st.Code() != tc.expectCd {
					t.Fatalf("expected code %v, got %v", tc.expectCd, st.Code())
				}
			}
		})
	}
}

func TestSubscribeFiltersRooms(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	auth := &authStub{allowed: true}
	orgID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, hub, server.WithAuthorizationClient(auth))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{fmt.Sprintf("organization:%s", orgID)}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	nonMatching := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{fmt.Sprintf("organization:%s", uuid.New())},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}
	matching := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt-match",
		Source: "src",
		Rooms:  []string{fmt.Sprintf("organization:%s", orgID)},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(2),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(nonMatching)
		hub.Broadcast(matching)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}

	if !proto.Equal(matching, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
	if len(auth.requests) != 0 {
		t.Fatalf("expected no authorization requests, got %d", len(auth.requests))
	}
}

func TestSubscribeCanonicalizesRooms(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	orgID := uuid.New()
	workloadID := uuid.New()
	store := server.NewWorkloadOrgIndex()
	store.RecordEnvelope(&notificationsv1.NotificationEnvelope{Rooms: []string{
		fmt.Sprintf("workload:%s", workloadID),
		fmt.Sprintf("organization:%s", orgID),
	}})
	client, cleanup := startTestServer(t, &publisherStub{}, hub,
		server.WithWorkloadOrgResolver(store),
		server.WithWorkloadOrgRecorder(store),
	)
	defer cleanup()

	requestRoom := fmt.Sprintf("workload: %s", workloadID)
	canonicalRoom := fmt.Sprintf("workload:%s", workloadID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{requestRoom}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{canonicalRoom},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(envelope)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if !proto.Equal(envelope, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
}

func TestSubscribeTraceCanonicalizesRooms(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	organizationID := uuid.New()
	traceID := "0123456789abcdef0123456789abcdef"
	store := server.NewTraceOrgIndex()
	store.RecordEnvelope(&notificationsv1.NotificationEnvelope{
		Rooms: []string{fmt.Sprintf("trace:%s", traceID)},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"organization_id": structpb.NewStringValue(organizationID.String()),
		}},
	})
	client, cleanup := startTestServer(t, &publisherStub{}, hub,
		server.WithTraceOrgResolver(store),
		server.WithTraceOrgRecorder(store),
	)
	defer cleanup()

	requestRoom := fmt.Sprintf("trace:%s", strings.ToUpper(traceID))
	canonicalRoom := fmt.Sprintf("trace:%s", traceID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{requestRoom}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{canonicalRoom},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(envelope)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if !proto.Equal(envelope, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
}

func TestSubscribeTraceRoomsInvalid(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	tests := []string{
		"trace:",
		"trace:1234",
		"trace:0123456789abcdef0123456789abcde",
		"trace:0123456789abcdef0123456789abcdef00",
		"trace:0123456789abcdef0123456789abcdeg",
	}

	for _, room := range tests {
		room := room
		t.Run(room, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
			if err == nil {
				_, err = streamClient.Recv()
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
			}
		})
	}
}

func TestSubscribeValidatesRooms(t *testing.T) {
	t.Parallel()

	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{})
	defer cleanup()

	ctx := context.Background()

	tests := map[string]struct {
		rooms []string
		code  codes.Code
	}{
		"empty rooms": {
			rooms: nil,
			code:  codes.InvalidArgument,
		},
		"blank room": {
			rooms: []string{" "},
			code:  codes.InvalidArgument,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: tc.rooms})
			if err == nil {
				_, err = streamClient.Recv()
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected status error, got %v", err)
			}
			if st.Code() != tc.code {
				t.Fatalf("expected code %v, got %v", tc.code, st.Code())
			}
		})
	}
}

func TestSubscribeContextCanceled(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	identityID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{fmt.Sprintf("thread_participant:%s", identityID)}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	cancel()
	_, err = streamClient.Recv()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected status error, got %v", err)
	}
	if st.Code() != codes.Canceled {
		t.Fatalf("expected canceled code, got %v", st.Code())
	}
}

func TestSubscribeThreadParticipantRoom(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	room := fmt.Sprintf("thread_participant:%s", callerID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{room},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(envelope)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if !proto.Equal(envelope, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
}

func TestSubscribeWorkloadRoom(t *testing.T) {
	t.Parallel()

	workloadID := uuid.New()
	room := fmt.Sprintf("workload:%s", workloadID)

	hub := stream.NewHub(2, zap.NewNop())
	authClient := &authStub{allowed: false}
	runnersClient := &runnersStub{workload: &runnersv1.Workload{OrganizationId: uuid.NewString()}}
	client, cleanup := startTestServer(t, &publisherStub{}, hub,
		server.WithAuthorizationClient(authClient),
		server.WithRunnersClient(runnersClient),
	)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(&notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Rooms: []string{room}})
	}()

	if _, err := streamClient.Recv(); err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if len(runnersClient.requests) != 0 {
		t.Fatalf("expected no workload lookups, got %d", len(runnersClient.requests))
	}
	if len(authClient.requests) != 0 {
		t.Fatalf("expected no authorization checks, got %d", len(authClient.requests))
	}
}

func TestSubscribeUnknownRoomAllowed(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"project:unknown"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{"project:unknown"},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(envelope)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if !proto.Equal(envelope, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
}

func TestSubscribeDoesNotRequireIdentity(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	room := fmt.Sprintf("workload:%s", uuid.NewString())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{room},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(1),
		}},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		hub.Broadcast(envelope)
	}()

	msg, err := streamClient.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if !proto.Equal(envelope, msg.GetEnvelope()) {
		t.Fatalf("unexpected envelope: %+v", msg.GetEnvelope())
	}
}

type publisherStub struct {
	envelope *notificationsv1.NotificationEnvelope
	err      error
}

func (p *publisherStub) Publish(ctx context.Context, envelope *notificationsv1.NotificationEnvelope) error {
	if p.err != nil {
		return p.err
	}
	p.envelope = proto.Clone(envelope).(*notificationsv1.NotificationEnvelope)
	return nil
}

type authStub struct {
	allowed  bool
	err      error
	requests []*authorizationv1.CheckRequest
}

func (a *authStub) Check(ctx context.Context, req *authorizationv1.CheckRequest, _ ...grpc.CallOption) (*authorizationv1.CheckResponse, error) {
	a.requests = append(a.requests, req)
	if a.err != nil {
		return nil, a.err
	}
	return &authorizationv1.CheckResponse{Allowed: a.allowed}, nil
}

type runnersStub struct {
	workload       *runnersv1.Workload
	err            error
	requests       []*runnersv1.GetWorkloadRequest
	identityHeader string
}

func (r *runnersStub) GetWorkload(ctx context.Context, req *runnersv1.GetWorkloadRequest, _ ...grpc.CallOption) (*runnersv1.GetWorkloadResponse, error) {
	r.requests = append(r.requests, req)
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		values := md.Get(identityMetadataKey)
		if len(values) > 0 {
			r.identityHeader = values[0]
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return &runnersv1.GetWorkloadResponse{Workload: r.workload}, nil
}

type agentsStub struct {
	agent          *agentsv1.Agent
	err            error
	requests       []*agentsv1.GetAgentRequest
	identityHeader string
}

func (a *agentsStub) GetAgent(ctx context.Context, req *agentsv1.GetAgentRequest, _ ...grpc.CallOption) (*agentsv1.GetAgentResponse, error) {
	a.requests = append(a.requests, req)
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		values := md.Get(identityMetadataKey)
		if len(values) > 0 {
			a.identityHeader = values[0]
		}
	}
	if a.err != nil {
		return nil, a.err
	}
	return &agentsv1.GetAgentResponse{Agent: a.agent}, nil
}

type noopHub struct{}

func (n *noopHub) Subscribe(_ []string) (<-chan *notificationsv1.NotificationEnvelope, func()) {
	ch := make(chan *notificationsv1.NotificationEnvelope)
	return ch, func() { close(ch) }
}

func startTestServer(t *testing.T, publisher server.Publisher, hub server.SubscriptionHub, opts ...server.Option) (notificationsv1.NotificationsServiceClient, func()) {
	t.Helper()

	listener := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	notificationsv1.RegisterNotificationsServiceServer(grpcServer, server.New(publisher, hub, zap.NewNop(), opts...))

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.Dial()
	}

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial bufnet: %v", err)
	}

	client := notificationsv1.NewNotificationsServiceClient(conn)
	cleanup := func() {
		conn.Close()
		listener.Close()
		grpcServer.Stop()
	}

	return client, cleanup
}
