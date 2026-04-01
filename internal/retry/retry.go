package retry

import (
	"context"
	"fmt"
	"time"
)

// Config defines the retry limits and backoff settings.
type Config struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// State describes a failed attempt that will be retried.
type State struct {
	Attempt     int
	MaxAttempts int
	Backoff     time.Duration
	Err         error
}

// Operation performs a retryable action.
type Operation func(ctx context.Context) error

// OnRetry is invoked after a failed attempt when another retry will occur.
type OnRetry func(State)

// WithBackoff retries the operation using exponential backoff until success,
// context cancellation, or the maximum number of attempts is reached.
func WithBackoff(ctx context.Context, cfg Config, operation Operation, onRetry OnRetry) error {
	if cfg.MaxAttempts < 1 {
		return fmt.Errorf("retry: MaxAttempts must be >= 1, got %d", cfg.MaxAttempts)
	}

	backoff := cfg.InitialBackoff
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := operation(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt == cfg.MaxAttempts {
			break
		}

		delay := backoff
		if delay > cfg.MaxBackoff {
			delay = cfg.MaxBackoff
		}

		if onRetry != nil {
			onRetry(State{
				Attempt:     attempt,
				MaxAttempts: cfg.MaxAttempts,
				Backoff:     delay,
				Err:         lastErr,
			})
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}

	return lastErr
}
