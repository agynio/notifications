package smoke

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

const (
	defaultChannel = "notifications.v1"
)

func TestSmoke(t *testing.T) {
	grpcAddr := os.Getenv("SMOKE_GRPC_ADDR")
	redisAddr := os.Getenv("SMOKE_REDIS_ADDR")

	if grpcAddr == "" || redisAddr == "" {
		t.Skip("smoke test requires SMOKE_GRPC_ADDR and SMOKE_REDIS_ADDR")
	}

	channel := os.Getenv("SMOKE_CHANNEL")
	if channel == "" {
		channel = defaultChannel
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial grpc: %v", err)
	}
	defer conn.Close()

	client := notificationsv1.NewNotificationsServiceClient(conn)

	opt, err := redis.ParseURL(redisAddr)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}

	rdb := redis.NewClient(opt)
	defer func() {
		_ = rdb.Close()
	}()

	t.Run("SubscribeReceivesRedisPublish", func(t *testing.T) {
		testSubscribeReceivesRedisPublish(t, ctx, client, rdb, channel)
	})

	t.Run("PublishEnqueuesEnvelopeToRedis", func(t *testing.T) {
		testPublishEnqueuesEnvelopeToRedis(t, ctx, client, rdb, channel)
	})
}

func testSubscribeReceivesRedisPublish(t *testing.T, ctx context.Context, client notificationsv1.NotificationsServiceClient, rdb *redis.Client, channel string) {
	streamCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	stream, err := client.Subscribe(streamCtx, &notificationsv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payloadStruct, err := structpb.NewStruct(map[string]interface{}{
		"msg": "hello-from-redis",
	})
	if err != nil {
		t.Fatalf("struct payload: %v", err)
	}

	envelope := &notificationsv1.NotificationEnvelope{
		Id: "smoke-redis", Ts: timestamppb.Now(), Source: "smoke", Event: "redis.publish", Rooms: []string{"smoke"}, Payload: payloadStruct,
	}

	marshaller := protojson.MarshalOptions{EmitUnpopulated: true}
	data, err := marshaller.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	if err := rdb.Publish(streamCtx, channel, data).Err(); err != nil {
		t.Fatalf("redis publish: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if got := resp.GetEnvelope().GetEvent(); got != envelope.Event {
		t.Fatalf("unexpected event: got %q want %q", got, envelope.Event)
	}
}

func testPublishEnqueuesEnvelopeToRedis(t *testing.T, ctx context.Context, client notificationsv1.NotificationsServiceClient, rdb *redis.Client, channel string) {
	pubsub := rdb.Subscribe(ctx, channel)
	defer func() {
		_ = pubsub.Close()
	}()

	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("receive subscription ack: %v", err)
	}

	payloadStruct, err := structpb.NewStruct(map[string]interface{}{
		"msg": "hello-from-grpc",
	})
	if err != nil {
		t.Fatalf("struct payload: %v", err)
	}

	publishReq := &notificationsv1.PublishRequest{
		Source:  "smoke",
		Event:   "grpc.publish",
		Rooms:   []string{"smoke"},
		Payload: payloadStruct,
	}

	publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := client.Publish(publishCtx, publishReq)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if resp.GetId() == "" {
		t.Fatalf("expected non-empty response id")
	}

	if resp.GetTs() == nil {
		t.Fatalf("expected timestamp in response")
	}

	msgCtx, cancelMsg := context.WithTimeout(ctx, 10*time.Second)
	defer cancelMsg()

	msg, err := pubsub.ReceiveMessage(msgCtx)
	if err != nil {
		t.Fatalf("receive redis message: %v", err)
	}

	received := new(notificationsv1.NotificationEnvelope)
	if err := protojson.Unmarshal([]byte(msg.Payload), received); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if received.GetEvent() != publishReq.GetEvent() {
		t.Fatalf("redis event mismatch: got %q want %q", received.GetEvent(), publishReq.GetEvent())
	}

	if len(received.GetRooms()) == 0 || received.GetRooms()[0] != "smoke" {
		t.Fatalf("unexpected rooms: %v", received.GetRooms())
	}
}
