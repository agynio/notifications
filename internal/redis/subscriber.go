package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	"github.com/agynio/notifications/internal/retry"
)

const (
	subscribeRetryMaxAttempts    = 15
	subscribeRetryInitialBackoff = 500 * time.Millisecond
	subscribeRetryMaxBackoff     = 5 * time.Second
)

type pubSub interface {
	Receive(ctx context.Context) (interface{}, error)
	ReceiveMessage(ctx context.Context) (*redis.Message, error)
	Close() error
}

type redisSubscriber interface {
	Subscribe(ctx context.Context, channels ...string) pubSub
}

type redisClientAdapter struct {
	client *redis.Client
}

func (a redisClientAdapter) Subscribe(ctx context.Context, channels ...string) pubSub {
	return a.client.Subscribe(ctx, channels...)
}

// Subscriber consumes notification envelopes from Redis and exposes them as a
// typed channel for downstream consumers.
type Subscriber struct {
	client  redisSubscriber
	channel string
	logger  *zap.Logger

	messages chan *notificationsv1.NotificationEnvelope
	mu       sync.Mutex
	started  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewSubscriber builds a Subscriber around the provided Redis client and
// channel. The logger is optional and may be nil.
func NewSubscriber(client *redis.Client, channel string, logger *zap.Logger) *Subscriber {
	return newSubscriber(redisClientAdapter{client: client}, channel, logger)
}

func newSubscriber(client redisSubscriber, channel string, logger *zap.Logger) *Subscriber {
	return &Subscriber{
		client:   client,
		channel:  channel,
		logger:   logger,
		messages: make(chan *notificationsv1.NotificationEnvelope),
	}
}

// Start begins streaming messages from Redis. Calling Start more than once
// results in an error. The provided context governs the lifetime of the
// streaming goroutine.
func (s *Subscriber) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("subscriber already started")
	}

	pubsub, err := s.subscribeWithRetry(ctx)
	if err != nil {
		return err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(s.messages)
		defer func() {
			_ = pubsub.Close()
		}()

		decoder := protojson.UnmarshalOptions{}
		for {
			select {
			case <-streamCtx.Done():
				return
			default:
			}

			msg, err := pubsub.ReceiveMessage(streamCtx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				s.logError("redis receive message", err)
				continue
			}

			envelope := new(notificationsv1.NotificationEnvelope)
			if err := decoder.Unmarshal([]byte(msg.Payload), envelope); err != nil {
				s.logError("decode envelope", err)
				continue
			}

			select {
			case <-streamCtx.Done():
				return
			case s.messages <- envelope:
			}
		}
	}()

	return nil
}

func (s *Subscriber) subscribeWithRetry(ctx context.Context) (pubSub, error) {
	var pubsub pubSub
	config := retry.Config{
		MaxAttempts:    subscribeRetryMaxAttempts,
		InitialBackoff: subscribeRetryInitialBackoff,
		MaxBackoff:     subscribeRetryMaxBackoff,
	}
	if err := retry.WithBackoff(ctx, config, func(ctx context.Context) error {
		current := s.client.Subscribe(ctx, s.channel)
		if current == nil {
			return fmt.Errorf("redis subscribe returned nil pubsub")
		}
		if _, err := current.Receive(ctx); err != nil {
			_ = current.Close()
			return fmt.Errorf("redis receive subscription ack: %w", err)
		}
		pubsub = current
		return nil
	}, func(state retry.State) {
		s.logWarn(
			"redis subscribe failed, retrying",
			zap.Int("attempt", state.Attempt),
			zap.Int("max_attempts", state.MaxAttempts),
			zap.Duration("backoff", state.Backoff),
			zap.Error(state.Err),
		)
	}); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	if pubsub == nil {
		return nil, fmt.Errorf("redis subscribe failed")
	}

	return pubsub, nil
}

// Stop terminates the streaming goroutine and waits for it to finish.
func (s *Subscriber) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	started := s.started
	s.cancel = nil
	s.started = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if started {
		s.wg.Wait()
	}
}

// Messages returns the live stream of envelopes. The channel closes when the
// subscriber stops.
func (s *Subscriber) Messages() <-chan *notificationsv1.NotificationEnvelope {
	return s.messages
}

func (s *Subscriber) logError(msg string, err error) {
	if s.logger != nil {
		s.logger.Error(msg, zap.Error(err))
	}
}

func (s *Subscriber) logWarn(msg string, fields ...zap.Field) {
	if s.logger != nil {
		s.logger.Warn(msg, fields...)
	}
}
