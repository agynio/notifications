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

	authorizationv1 "github.com/agynio/notifications/internal/.gen/agynio/api/authorization/v1"
	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	"github.com/agynio/notifications/internal/server"
	"github.com/agynio/notifications/internal/stream"
)

const bufSize = 1024 * 1024
const identityMetadataKey = "x-identity-id"

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

func TestSubscribe(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"room"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     uuid.NewString(),
		Ts:     timestamppb.Now(),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{"room"},
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

func TestSubscribeCanonicalizesRooms(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	workloadID := uuid.New()
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
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	traceID := "0123456789abcdef0123456789abcdef"
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

func TestSubscribeContextCanceled(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"room"}})
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

func TestSubscribeThreadParticipantAuthorization(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	room := fmt.Sprintf("thread_participant:%s", callerID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, callerID.String())

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

	badCtx := metadata.AppendToOutgoingContext(context.Background(), identityMetadataKey, uuid.NewString())
	badStream, err := client.Subscribe(badCtx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	_, err = badStream.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied error, got %v", err)
	}
}

func TestSubscribeWorkloadAuthorization(t *testing.T) {
	t.Parallel()

	workloadID := uuid.New()
	organizationID := uuid.New()
	callerID := uuid.New()
	room := fmt.Sprintf("workload:%s", workloadID)
	store := server.NewWorkloadOrgIndex()
	store.RecordEnvelope(&notificationsv1.NotificationEnvelope{Rooms: []string{
		fmt.Sprintf("organization:%s", organizationID),
		room,
	}})

	tests := []struct {
		name       string
		allowed    bool
		expectCode codes.Code
	}{
		{name: "allowed", allowed: true, expectCode: codes.OK},
		{name: "denied", allowed: false, expectCode: codes.PermissionDenied},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hub := stream.NewHub(2, zap.NewNop())
			checkCh := make(chan *authorizationv1.CheckRequest, 1)
			authClient := fakeAuthorizationClient{
				check: func(ctx context.Context, req *authorizationv1.CheckRequest) (*authorizationv1.CheckResponse, error) {
					checkCh <- req
					return &authorizationv1.CheckResponse{Allowed: tc.allowed}, nil
				},
			}
			client, cleanup := startTestServer(t, &publisherStub{}, hub,
				server.WithAuthorizationClient(authClient),
				server.WithWorkloadOrgResolver(store),
				server.WithWorkloadOrgRecorder(store),
			)
			defer cleanup()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, callerID.String())
			streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
			if err != nil {
				t.Fatalf("Subscribe returned error: %v", err)
			}

			if tc.expectCode != codes.OK {
				_, err := streamClient.Recv()
				if status.Code(err) != tc.expectCode {
					t.Fatalf("expected code %v, got %v", tc.expectCode, status.Code(err))
				}
				select {
				case <-checkCh:
				case <-time.After(time.Second):
					t.Fatal("expected authorization check")
				}
				return
			}

			go func() {
				time.Sleep(10 * time.Millisecond)
				hub.Broadcast(&notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Rooms: []string{room}})
			}()

			if _, err := streamClient.Recv(); err != nil {
				t.Fatalf("Recv returned error: %v", err)
			}

			select {
			case gotCheck := <-checkCh:
				if gotCheck.GetTupleKey().GetObject() != fmt.Sprintf("organization:%s", organizationID) {
					t.Fatalf("unexpected object: %s", gotCheck.GetTupleKey().GetObject())
				}
			case <-time.After(time.Second):
				t.Fatal("expected authorization check")
			}
		})
	}
}

func TestSubscribeTraceAuthorization(t *testing.T) {
	t.Parallel()

	traceID := "0123456789abcdef0123456789abcdef"
	organizationID := uuid.New()
	callerID := uuid.New()
	room := fmt.Sprintf("trace:%s", traceID)
	store := server.NewTraceOrgIndex()
	store.RecordEnvelope(&notificationsv1.NotificationEnvelope{
		Rooms: []string{room},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"organization_id": structpb.NewStringValue(organizationID.String()),
		}},
	})

	tests := []struct {
		name       string
		allowed    bool
		expectCode codes.Code
	}{
		{name: "allowed", allowed: true, expectCode: codes.OK},
		{name: "denied", allowed: false, expectCode: codes.PermissionDenied},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hub := stream.NewHub(2, zap.NewNop())
			checkCh := make(chan *authorizationv1.CheckRequest, 1)
			authClient := fakeAuthorizationClient{
				check: func(ctx context.Context, req *authorizationv1.CheckRequest) (*authorizationv1.CheckResponse, error) {
					checkCh <- req
					return &authorizationv1.CheckResponse{Allowed: tc.allowed}, nil
				},
			}
			client, cleanup := startTestServer(t, &publisherStub{}, hub,
				server.WithAuthorizationClient(authClient),
				server.WithTraceOrgResolver(store),
				server.WithTraceOrgRecorder(store),
			)
			defer cleanup()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, callerID.String())
			streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
			if err != nil {
				t.Fatalf("Subscribe returned error: %v", err)
			}

			if tc.expectCode != codes.OK {
				_, err := streamClient.Recv()
				if status.Code(err) != tc.expectCode {
					t.Fatalf("expected code %v, got %v", tc.expectCode, status.Code(err))
				}
				select {
				case <-checkCh:
				case <-time.After(time.Second):
					t.Fatal("expected authorization check")
				}
				return
			}

			go func() {
				time.Sleep(10 * time.Millisecond)
				hub.Broadcast(&notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Rooms: []string{room}})
			}()

			if _, err := streamClient.Recv(); err != nil {
				t.Fatalf("Recv returned error: %v", err)
			}

			select {
			case gotCheck := <-checkCh:
				if gotCheck.GetTupleKey().GetObject() != fmt.Sprintf("organization:%s", organizationID) {
					t.Fatalf("unexpected object: %s", gotCheck.GetTupleKey().GetObject())
				}
			case <-time.After(time.Second):
				t.Fatal("expected authorization check")
			}
		})
	}
}

func TestSubscribeTraceAuthorizationMissingMapping(t *testing.T) {
	t.Parallel()

	traceID := "0123456789abcdef0123456789abcdef"
	callerID := uuid.New()
	room := fmt.Sprintf("trace:%s", traceID)
	store := server.NewTraceOrgIndex()

	hub := stream.NewHub(2, zap.NewNop())
	checkCh := make(chan *authorizationv1.CheckRequest, 1)
	authClient := fakeAuthorizationClient{
		check: func(ctx context.Context, req *authorizationv1.CheckRequest) (*authorizationv1.CheckResponse, error) {
			checkCh <- req
			return &authorizationv1.CheckResponse{Allowed: true}, nil
		},
	}
	client, cleanup := startTestServer(t, &publisherStub{}, hub,
		server.WithAuthorizationClient(authClient),
		server.WithTraceOrgResolver(store),
		server.WithTraceOrgRecorder(store),
	)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, callerID.String())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	_, err = streamClient.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}

	select {
	case <-checkCh:
		t.Fatal("unexpected authorization check")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscribeOrganizationAuthorization(t *testing.T) {
	t.Parallel()

	organizationID := uuid.New()
	callerID := uuid.New()
	room := fmt.Sprintf("organization:%s", organizationID)

	tests := []struct {
		name       string
		allowed    bool
		expectCode codes.Code
	}{
		{name: "allowed", allowed: true, expectCode: codes.OK},
		{name: "denied", allowed: false, expectCode: codes.PermissionDenied},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hub := stream.NewHub(2, zap.NewNop())
			checkCh := make(chan *authorizationv1.CheckRequest, 1)
			authClient := fakeAuthorizationClient{
				check: func(ctx context.Context, req *authorizationv1.CheckRequest) (*authorizationv1.CheckResponse, error) {
					checkCh <- req
					return &authorizationv1.CheckResponse{Allowed: tc.allowed}, nil
				},
			}
			client, cleanup := startTestServer(t, &publisherStub{}, hub,
				server.WithAuthorizationClient(authClient),
			)
			defer cleanup()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, callerID.String())
			streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
			if err != nil {
				t.Fatalf("Subscribe returned error: %v", err)
			}

			if tc.expectCode != codes.OK {
				_, err := streamClient.Recv()
				if status.Code(err) != tc.expectCode {
					t.Fatalf("expected code %v, got %v", tc.expectCode, status.Code(err))
				}
				select {
				case <-checkCh:
				case <-time.After(time.Second):
					t.Fatal("expected authorization check")
				}
				return
			}

			go func() {
				time.Sleep(10 * time.Millisecond)
				hub.Broadcast(&notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Rooms: []string{room}})
			}()

			if _, err := streamClient.Recv(); err != nil {
				t.Fatalf("Recv returned error: %v", err)
			}

			select {
			case gotCheck := <-checkCh:
				if gotCheck.GetTupleKey().GetObject() != fmt.Sprintf("organization:%s", organizationID) {
					t.Fatalf("unexpected object: %s", gotCheck.GetTupleKey().GetObject())
				}
			case <-time.After(time.Second):
				t.Fatal("expected authorization check")
			}
		})
	}
}

func TestSubscribeUnknownRoomAuthorization(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, identityMetadataKey, uuid.NewString())

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"agent:unknown"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	_, err = streamClient.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied error, got %v", status.Code(err))
	}
}

func TestSubscribeInternalBypassesAuthorization(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{fmt.Sprintf("workload:%s", uuid.NewString())}})
	if err != nil {
		cancel()
		t.Fatalf("Subscribe returned error: %v", err)
	}

	cancel()
	_, err = streamClient.Recv()
	if status.Code(err) != codes.Canceled {
		t.Fatalf("expected canceled code, got %v", status.Code(err))
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

type fakeAuthorizationClient struct {
	check func(context.Context, *authorizationv1.CheckRequest) (*authorizationv1.CheckResponse, error)
}

func (f fakeAuthorizationClient) Check(ctx context.Context, req *authorizationv1.CheckRequest, opts ...grpc.CallOption) (*authorizationv1.CheckResponse, error) {
	if f.check == nil {
		return &authorizationv1.CheckResponse{}, nil
	}
	return f.check(ctx, req)
}

func (f fakeAuthorizationClient) BatchCheck(ctx context.Context, req *authorizationv1.BatchCheckRequest, opts ...grpc.CallOption) (*authorizationv1.BatchCheckResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (f fakeAuthorizationClient) Write(ctx context.Context, req *authorizationv1.WriteRequest, opts ...grpc.CallOption) (*authorizationv1.WriteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (f fakeAuthorizationClient) Read(ctx context.Context, req *authorizationv1.ReadRequest, opts ...grpc.CallOption) (*authorizationv1.ReadResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (f fakeAuthorizationClient) ListObjects(ctx context.Context, req *authorizationv1.ListObjectsRequest, opts ...grpc.CallOption) (*authorizationv1.ListObjectsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

func (f fakeAuthorizationClient) ListUsers(ctx context.Context, req *authorizationv1.ListUsersRequest, opts ...grpc.CallOption) (*authorizationv1.ListUsersResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

type noopHub struct{}

func (n *noopHub) Subscribe(rooms []string) (<-chan *notificationsv1.NotificationEnvelope, func()) {
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
