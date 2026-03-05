package redis

import (
	"context"
	"fmt"

	redis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

type redisPublisher interface {
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd
}

// Publisher publishes notification envelopes to Redis channels.
type Publisher struct {
	client  redisPublisher
	channel string
}

// NewPublisher constructs a Publisher for the given Redis client and channel.
func NewPublisher(client redisPublisher, channel string) *Publisher {
	return &Publisher{client: client, channel: channel}
}

// Publish serialises the envelope to JSON and forwards it to Redis.
func (p *Publisher) Publish(ctx context.Context, envelope *notificationsv1.NotificationEnvelope) error {
	if envelope == nil {
		return fmt.Errorf("nil envelope")
	}

	marshaller := protojson.MarshalOptions{
		EmitUnpopulated: true,
	}
	payload, err := marshaller.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	if err := p.client.Publish(ctx, p.channel, payload).Err(); err != nil {
		return fmt.Errorf("redis publish: %w", err)
	}
	return nil
}
