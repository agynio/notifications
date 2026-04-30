package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	"github.com/agynio/notifications/internal/config"
	"github.com/agynio/notifications/internal/logging"
	redisstream "github.com/agynio/notifications/internal/redis"
	"github.com/agynio/notifications/internal/retry"
	"github.com/agynio/notifications/internal/server"
	"github.com/agynio/notifications/internal/stream"
	redis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const (
	redisRetryMaxAttempts    = 15
	redisRetryInitialBackoff = 500 * time.Millisecond
	redisRetryMaxBackoff     = 5 * time.Second
	redisPingTimeout         = 3 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "notifications service failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	pubClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer func() { _ = pubClient.Close() }()

	subClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer func() { _ = subClient.Close() }()

	if err := ensureRedis(ctx, logger, pubClient); err != nil {
		return err
	}

	publisher := redisstream.NewPublisher(pubClient, cfg.RedisChannel)
	subscriber := redisstream.NewSubscriber(subClient, cfg.RedisChannel, logger)
	if err := subscriber.Start(ctx); err != nil {
		return fmt.Errorf("start redis subscriber: %w", err)
	}

	hub := stream.NewHub(cfg.StreamBufferSize, logger)

	forwardCtx, forwardCancel := context.WithCancel(ctx)
	var forwardWG sync.WaitGroup
	forwardWG.Add(1)
	go func() {
		defer forwardWG.Done()
		for {
			select {
			case <-forwardCtx.Done():
				return
			case envelope, ok := <-subscriber.Messages():
				if !ok {
					return
				}
				hub.Broadcast(envelope)
			}
		}
	}()

	grpcServer := grpc.NewServer()
	notificationsv1.RegisterNotificationsServiceServer(
		grpcServer,
		server.New(publisher, hub, logger,
			server.WithClock(func() time.Time { return time.Now().UTC() }),
		),
	)

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		forwardCancel()
		subscriber.Stop()
		return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}

	defer forwardCancel()
	defer forwardWG.Wait()
	defer subscriber.Stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("gRPC server starting", zap.String("addr", cfg.GRPCAddr))
		err := grpcServer.Serve(listener)
		if errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
		serverErr <- err
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		grpcServer.GracefulStop()
		err := <-serverErr
		if err != nil {
			return err
		}
	}

	return nil
}

func ensureRedis(ctx context.Context, logger *zap.Logger, client *redis.Client) error {
	config := retry.Config{
		MaxAttempts:    redisRetryMaxAttempts,
		InitialBackoff: redisRetryInitialBackoff,
		MaxBackoff:     redisRetryMaxBackoff,
	}
	if err := retry.WithBackoff(ctx, config, func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
		defer cancel()
		return client.Ping(pingCtx).Err()
	}, func(state retry.State) {
		if logger == nil {
			return
		}
		logger.Warn(
			"redis ping failed, retrying",
			zap.Int("attempt", state.Attempt),
			zap.Int("max_attempts", state.MaxAttempts),
			zap.Duration("backoff", state.Backoff),
			zap.Error(state.Err),
		)
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("redis ping: %w", err)
	}

	return nil
}
