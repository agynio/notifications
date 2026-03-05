package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

func TestSubscriber_StartAndReceive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer func() { _ = client.Close() }()

	subscriber := NewSubscriber(client, "notifications", zap.NewNop())
	if err := subscriber.Start(ctx); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer subscriber.Stop()

	publisherClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer func() { _ = publisherClient.Close() }()
	publisher := NewPublisher(publisherClient, "notifications")

	envelope := &notificationsv1.NotificationEnvelope{
		Id:     "id",
		Ts:     timestamppb.New(time.Now().UTC()),
		Event:  "evt",
		Source: "src",
		Rooms:  []string{"room"},
		Payload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"value": structpb.NewNumberValue(42),
		}},
	}

	if err := publisher.Publish(ctx, envelope); err != nil {
		t.Fatalf("publisher.Publish returned error: %v", err)
	}

	select {
	case <-ctx.Done():
		t.Fatalf("context done before message: %v", ctx.Err())
	case msg, ok := <-subscriber.Messages():
		if !ok {
			t.Fatal("messages channel closed unexpectedly")
		}
		if msg.GetId() != envelope.GetId() {
			t.Fatalf("unexpected envelope id: got %s want %s", msg.GetId(), envelope.GetId())
		}
	}
}

func TestSubscriber_Start_ErrorOnSubscribe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mock := &mockSubscriber{
		sub: &mockPubSub{
			receiveErr: errors.New("subscribe failed"),
		},
	}

	subscriber := newSubscriber(mock, "channel", zap.NewNop())
	if err := subscriber.Start(ctx); err == nil {
		t.Fatal("expected error, got nil")
	}
}

type mockSubscriber struct {
	sub pubSub
}

func (m *mockSubscriber) Subscribe(ctx context.Context, channels ...string) pubSub {
	return m.sub
}

type mockPubSub struct {
	receiveErr        error
	receiveMessage    *redis.Message
	receiveMessageErr error
}

func (m *mockPubSub) Receive(ctx context.Context) (interface{}, error) {
	return nil, m.receiveErr
}

func (m *mockPubSub) ReceiveMessage(ctx context.Context) (*redis.Message, error) {
	if m.receiveMessage != nil {
		return m.receiveMessage, nil
	}
	return nil, m.receiveMessageErr
}

func (m *mockPubSub) Close() error { return nil }
