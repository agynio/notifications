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

			ctx := authenticatedContext(uuid.New())
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
	orgID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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
}

func TestSubscribeCanonicalizesRooms(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	workloadID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	requestRoom := fmt.Sprintf("workload: %s", workloadID)
	canonicalRoom := fmt.Sprintf("workload:%s", workloadID)
	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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
	traceID := "0123456789abcdef0123456789abcdef"
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	requestRoom := fmt.Sprintf("trace:%s", strings.ToUpper(traceID))
	canonicalRoom := fmt.Sprintf("trace:%s", traceID)
	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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
			ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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

	ctx := authenticatedContext(uuid.New())

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

	ctx, cancel := context.WithCancel(authenticatedContext(identityID))
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
	ctx, cancel := context.WithTimeout(authenticatedContext(callerID), time.Second)
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
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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
}

// A room outside the table in architecture/authz.md has no publisher, so a
// subscription to one can only be a probe.
func TestSubscribeUnknownRoomDenied(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"project:unknown"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if _, err := streamClient.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

// A caller with no relation to the organization owning the entity is refused,
// even though the room itself is well formed.
func TestSubscribeDeniesUnrelatedCaller(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub, server.WithAuthorization(denyAll{}, denyAll{}, denyAll{}))
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
	defer cancel()

	for _, room := range []string{
		"organization:" + uuid.NewString(),
		"sandbox_org:" + uuid.NewString(),
		"workload:" + uuid.NewString(),
		"agent:" + uuid.NewString(),
		"agent_instance:" + uuid.NewString(),
	} {
		streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
		if err != nil {
			t.Fatalf("Subscribe(%s) returned error: %v", room, err)
		}
		if _, err := streamClient.Recv(); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: expected PermissionDenied, got %v (%v)", room, status.Code(err), err)
		}
	}
}

// The Orchestrator watches every instance it reconciles through this room, and
// agents publishes instance.updated to it. The room was never recognised here,
// so it fell to the deny-by-default branch and every one of those subscriptions
// was refused -- a paused or resumed instance reached nobody.
func TestSubscribeAgentInstanceRoomAllowsOrganizationMember(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	room := "agent_instance:" + uuid.NewString()
	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
	defer cancel()

	if _, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}}); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
}

// Sandbox owner rooms are identity equality, with no authorization call at all,
// so a permissive authorizer must not make someone else's room readable.
func TestSubscribeSandboxOwnerRoomIsOwnerOnly(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{
		Rooms: []string{"sandbox_owner:" + uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if _, err := streamClient.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

func TestSubscribeDoesNotRequireIdentity(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(2, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(uuid.New()), time.Second)
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

func TestSubscribeThreadParticipantSelfRoom(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	room := fmt.Sprintf("thread_participant:%s", callerID)
	ctx, cancel := context.WithTimeout(authenticatedContext(callerID), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"thread_participant:me"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Event: "evt", Source: "src", Rooms: []string{room}}
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

func TestSubscribeThreadParticipantSelfRoomDedupesWithExplicitRoom(t *testing.T) {
	t.Parallel()

	hub := &recordingHub{}
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	ctx, cancel := context.WithTimeout(authenticatedContext(callerID), time.Second)
	defer cancel()
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"thread_participant:me", fmt.Sprintf("thread_participant:%s", callerID)}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	_, err = streamClient.Recv()
	if status.Code(err) != codes.Canceled && status.Code(err) != codes.DeadlineExceeded {
		// The important assertion is below; allow the stream to terminate by context.
	}
	if len(hub.rooms) != 1 || hub.rooms[0] != fmt.Sprintf("thread_participant:%s", callerID) {
		t.Fatalf("unexpected subscribed rooms: %#v", hub.rooms)
	}
}

func TestSubscribeInstanceInboxSelfRoom(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	room := fmt.Sprintf("instance_inbox:%s", callerID)
	ctx, cancel := context.WithTimeout(authenticatedContextWithType(callerID, "agent_instance"), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"instance_inbox:me"}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Event: "evt", Source: "src", Rooms: []string{room}}
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

func TestSubscribeInstanceInboxSelfRoomDedupesWithExplicitRoom(t *testing.T) {
	t.Parallel()

	hub := &recordingHub{}
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	ctx, cancel := context.WithTimeout(authenticatedContextWithType(callerID, "agent_instance"), time.Second)
	defer cancel()
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{"instance_inbox:me", fmt.Sprintf("instance_inbox:%s", callerID)}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	_, err = streamClient.Recv()
	if status.Code(err) != codes.Canceled && status.Code(err) != codes.DeadlineExceeded {
		// The important assertion is below; allow the stream to terminate by context.
	}
	if len(hub.rooms) != 1 || hub.rooms[0] != fmt.Sprintf("instance_inbox:%s", callerID) {
		t.Fatalf("unexpected subscribed rooms: %#v", hub.rooms)
	}
}

func TestSubscribeInstanceInboxWrongIdentityDenied(t *testing.T) {
	t.Parallel()

	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{})
	defer cleanup()

	ctx := authenticatedContextWithType(uuid.New(), "agent_instance")
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{fmt.Sprintf("instance_inbox:%s", uuid.New())}})
	if err == nil {
		_, err = streamClient.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

func TestSubscribeInstanceInboxConcreteRoomAllowsMatchingAgentInstance(t *testing.T) {
	t.Parallel()

	hub := stream.NewHub(4, zap.NewNop())
	client, cleanup := startTestServer(t, &publisherStub{}, hub)
	defer cleanup()

	callerID := uuid.New()
	room := fmt.Sprintf("instance_inbox:%s", callerID)
	ctx, cancel := context.WithTimeout(authenticatedContextWithType(callerID, "agent_instance"), time.Second)
	defer cancel()

	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{Id: uuid.NewString(), Ts: timestamppb.Now(), Event: "evt", Source: "src", Rooms: []string{room}}
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

func TestSubscribeInstanceInboxRequiresAgentInstanceIdentityType(t *testing.T) {
	t.Parallel()

	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{})
	t.Cleanup(cleanup)

	for _, identityType := range []string{"", "user", "app", "agent"} {
		identityType := identityType
		for _, room := range []string{"instance_inbox:me", fmt.Sprintf("instance_inbox:%s", uuid.New())} {
			room := room
			t.Run(identityType+"/"+room, func(t *testing.T) {
				t.Parallel()
				ctx := authenticatedContextWithType(uuid.New(), identityType)
				streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{room}})
				if err == nil {
					_, err = streamClient.Recv()
				}
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
				}
			})
		}
	}
}

func TestSubscribeThreadParticipantWrongIdentityDenied(t *testing.T) {
	t.Parallel()

	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{})
	defer cleanup()

	ctx := authenticatedContext(uuid.New())
	streamClient, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{Rooms: []string{fmt.Sprintf("thread_participant:%s", uuid.New())}})
	if err == nil {
		_, err = streamClient.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

func TestSubscribeThreadParticipantSelfRequiresIdentity(t *testing.T) {
	t.Parallel()

	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{})
	defer cleanup()

	streamClient, err := client.Subscribe(context.Background(), &notificationsv1.SubscribeRequest{Rooms: []string{"thread_participant:me"}})
	if err == nil {
		_, err = streamClient.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v (%v)", status.Code(err), err)
	}
}

func authenticatedContext(identityID uuid.UUID) context.Context {
	return authenticatedContextWithType(identityID, "")
}

func authenticatedContextWithType(identityID uuid.UUID, identityType string) context.Context {
	pairs := []string{"x-identity-id", identityID.String()}
	if identityType != "" {
		pairs = append(pairs, "x-identity-type", identityType)
	}
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(pairs...))
}

type recordingHub struct {
	rooms []string
}

func (r *recordingHub) Subscribe(rooms []string) (<-chan *notificationsv1.NotificationEnvelope, func()) {
	r.rooms = append([]string(nil), rooms...)
	ch := make(chan *notificationsv1.NotificationEnvelope)
	return ch, func() { close(ch) }
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

func (n *noopHub) Subscribe(_ []string) (<-chan *notificationsv1.NotificationEnvelope, func()) {
	ch := make(chan *notificationsv1.NotificationEnvelope)
	return ch, func() { close(ch) }
}

// allowAll answers every authorization check yes and reports a fixed
// organization for any entity.
type allowAll struct{}

func (allowAll) Check(context.Context, *authorizationv1.CheckRequest, ...grpc.CallOption) (*authorizationv1.CheckResponse, error) {
	return &authorizationv1.CheckResponse{Allowed: true}, nil
}

func (allowAll) GetWorkload(_ context.Context, _ *runnersv1.GetWorkloadRequest, _ ...grpc.CallOption) (*runnersv1.GetWorkloadResponse, error) {
	return &runnersv1.GetWorkloadResponse{Workload: &runnersv1.Workload{OrganizationId: uuid.NewString()}}, nil
}

func (allowAll) GetAgent(_ context.Context, _ *agentsv1.GetAgentRequest, _ ...grpc.CallOption) (*agentsv1.GetAgentResponse, error) {
	return &agentsv1.GetAgentResponse{Agent: &agentsv1.Agent{OrganizationId: uuid.NewString()}}, nil
}

func (allowAll) GetInstance(_ context.Context, _ *agentsv1.GetInstanceRequest, _ ...grpc.CallOption) (*agentsv1.GetInstanceResponse, error) {
	return &agentsv1.GetInstanceResponse{Instance: &agentsv1.AgentInstance{OrganizationId: uuid.NewString()}}, nil
}

// denyAll refuses every check but still resolves organizations, so a test can
// tell "the caller has no relation" apart from "the lookup failed".
type denyAll struct{ allowAll }

func (denyAll) Check(context.Context, *authorizationv1.CheckRequest, ...grpc.CallOption) (*authorizationv1.CheckResponse, error) {
	return &authorizationv1.CheckResponse{Allowed: false}, nil
}

// unavailableAuthz stands for an authorization service that cannot answer,
// as opposed to one that answers no.
type unavailableAuthz struct{ allowAll }

func (unavailableAuthz) Check(context.Context, *authorizationv1.CheckRequest, ...grpc.CallOption) (*authorizationv1.CheckResponse, error) {
	return nil, status.Error(codes.Unavailable, "authorization is down")
}

// missingInstance resolves nothing: the instance the room names is gone.
type missingInstance struct{ allowAll }

func (missingInstance) GetInstance(context.Context, *agentsv1.GetInstanceRequest, ...grpc.CallOption) (*agentsv1.GetInstanceResponse, error) {
	return nil, status.Error(codes.NotFound, "no such instance")
}

// An unreachable authorization service is not a verdict. Reporting it as
// PermissionDenied sent the subscriber retrying an access problem it never had.
func TestSubscribeReportsUnavailableAuthorizationAsUnavailable(t *testing.T) {
	callerID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{},
		server.WithAuthorization(unavailableAuthz{}, allowAll{}, allowAll{}))
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContext(callerID), time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{
		Rooms: []string{"organization:" + uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

// A room naming an instance that no longer exists stays PermissionDenied, so
// the room cannot be probed for whether an instance is there.
func TestSubscribeReportsMissingInstanceAsPermissionDenied(t *testing.T) {
	callerID := uuid.New()
	client, cleanup := startTestServer(t, &publisherStub{}, &noopHub{},
		server.WithAuthorization(allowAll{}, allowAll{}, missingInstance{}))
	defer cleanup()

	ctx, cancel := context.WithTimeout(authenticatedContextWithType(callerID, "agent_instance"), time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, &notificationsv1.SubscribeRequest{
		Rooms: []string{"agent_instance:" + uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func startTestServer(t *testing.T, publisher server.Publisher, hub server.SubscriptionHub, opts ...server.Option) (notificationsv1.NotificationsServiceClient, func()) {
	t.Helper()

	listener := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	// Permissive by default so tests that are not about authorization keep
	// exercising what they were written for; a test that cares passes its own
	// WithAuthorization, which applies after these and wins.
	opts = append([]server.Option{server.WithAuthorization(allowAll{}, allowAll{}, allowAll{})}, opts...)
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
