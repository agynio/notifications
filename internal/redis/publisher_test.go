package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

func TestPublisher_Publish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer func() { _ = client.Close() }()

	channel := "notifications"
	pubsub := client.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = pubsub.Close() })

	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("receive subscribe ack: %v", err)
	}

	publisher := NewPublisher(client, channel)
	envelope := &notificationsv1.NotificationEnvelope{
		Id:     "test-id",
		Ts:     timestamppb.New(time.Now().UTC()),
		Source: "source",
		Event:  "event",
		Rooms:  []string{"room-a"},
		Payload: &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"key": structpb.NewStringValue("value"),
			},
		},
	}

	if err := publisher.Publish(ctx, envelope); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	msg, err := pubsub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage returned error: %v", err)
	}

	var decoded notificationsv1.NotificationEnvelope
	if err := protojson.Unmarshal([]byte(msg.Payload), &decoded); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}

	if !proto.Equal(envelope, &decoded) {
		t.Fatalf("unexpected envelope: got %+v want %+v", &decoded, envelope)
	}
}

func TestPublisher_Publish_Error(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mock := &mockRedisPublisher{err: errors.New("boom")}
	publisher := NewPublisher(mock, "channel")

	err := publisher.Publish(ctx, &notificationsv1.NotificationEnvelope{Id: "id", Ts: timestamppb.New(time.Now().UTC())})
	if err == nil || !errors.Is(err, mock.err) {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

type mockRedisPublisher struct {
	err error
}

func (m *mockRedisPublisher) Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd {
	return redis.NewIntResult(0, m.err)
}
