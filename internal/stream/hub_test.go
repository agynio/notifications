package stream

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

func TestHubBroadcastDeliversToSubscribers(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	hub := NewHub(2, logger)

	ch1, cancel1 := hub.Subscribe()
	defer cancel1()

	ch2, cancel2 := hub.Subscribe()
	defer cancel2()

	envelope := &notificationsv1.NotificationEnvelope{Id: "id", Ts: timestamppb.Now()}

	hub.Broadcast(envelope)

	select {
	case msg := <-ch1:
		if msg.GetId() != envelope.GetId() {
			t.Fatalf("unexpected id: got %s want %s", msg.GetId(), envelope.GetId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber 1")
	}

	select {
	case msg := <-ch2:
		if msg.GetId() != envelope.GetId() {
			t.Fatalf("unexpected id: got %s want %s", msg.GetId(), envelope.GetId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber 2")
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop()
	hub := NewHub(1, logger)

	chSlow, _ := hub.Subscribe()
	chFast, cancelFast := hub.Subscribe()
	defer cancelFast()

	envelope1 := &notificationsv1.NotificationEnvelope{Id: "event-1", Ts: timestamppb.Now()}
	envelope2 := &notificationsv1.NotificationEnvelope{Id: "event-2", Ts: timestamppb.Now()}

	hub.Broadcast(envelope1)
	hub.Broadcast(envelope2)

	if msg, ok := <-chSlow; !ok || msg.GetId() != envelope1.GetId() {
		t.Fatalf("expected buffered message before closure")
	}

	if _, ok := <-chSlow; ok {
		t.Fatal("expected slow subscriber channel to be closed after draining")
	}

	select {
	case msg := <-chFast:
		if msg.GetId() != envelope1.GetId() {
			t.Fatalf("unexpected id: got %s want %s", msg.GetId(), envelope1.GetId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fast subscriber")
	}
}
