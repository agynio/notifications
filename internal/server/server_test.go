package server_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	"github.com/agynio/notifications/internal/server"
	"github.com/agynio/notifications/internal/stream"
)

const bufSize = 1024 * 1024

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

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{})
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

func TestSubscribeContextCanceled(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{})
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

type noopHub struct{}

func (n *noopHub) Subscribe() (<-chan *notificationsv1.NotificationEnvelope, func()) {
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
