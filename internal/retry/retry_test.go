package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithBackoffSucceedsAfterRetries(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	config := Config{
		MaxAttempts:    4,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	}

	attempts := 0
	var states []State

	err := WithBackoff(ctx, config, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient failure")
		}
		return nil
	}, func(state State) {
		states = append(states, state)
	})

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 retry callbacks, got %d", len(states))
	}
	if states[0].Attempt != 1 || states[1].Attempt != 2 {
		t.Fatalf("unexpected retry attempts: %+v", states)
	}
	if states[0].Backoff != 5*time.Millisecond {
		t.Fatalf("unexpected first backoff: %v", states[0].Backoff)
	}
	if states[1].Backoff != 10*time.Millisecond {
		t.Fatalf("unexpected second backoff: %v", states[1].Backoff)
	}
}

func TestWithBackoffReturnsLastError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	config := Config{
		MaxAttempts:    3,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     7 * time.Millisecond,
	}

	sentinel := errors.New("persistent failure")
	var states []State
	attempts := 0

	err := WithBackoff(ctx, config, func(ctx context.Context) error {
		attempts++
		return sentinel
	}, func(state State) {
		states = append(states, state)
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error %q, got %v", sentinel, err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 retry callbacks, got %d", len(states))
	}
	if states[0].Backoff != 5*time.Millisecond {
		t.Fatalf("unexpected first backoff: %v", states[0].Backoff)
	}
	if states[1].Backoff != 7*time.Millisecond {
		t.Fatalf("unexpected capped backoff: %v", states[1].Backoff)
	}
}

func TestWithBackoffStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	config := Config{
		MaxAttempts:    2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}

	attempts := 0
	called := false

	err := WithBackoff(ctx, config, func(ctx context.Context) error {
		attempts++
		return nil
	}, func(state State) {
		called = true
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if attempts != 0 {
		t.Fatalf("expected no attempts, got %d", attempts)
	}
	if called {
		t.Fatal("unexpected retry callback")
	}
}
