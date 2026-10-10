package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// The slog bridge maps level 12 to OTLP severity 21; Alloy labels it FATAL.
const levelCritical = slog.LevelError + (slog.LevelError - slog.LevelWarn)

type event struct {
	Level      slog.Level
	Message    string
	Scenario   string
	Err        error
	RunbookURL string
	// ExcludeFromGeneralRule leaves alerting to a dedicated metric- or log-based rule.
	ExcludeFromGeneralRule bool
}

type databaseError struct{ message string }

func (e databaseError) Error() string { return e.message }

type paymentError struct{ message string }

func (e paymentError) Error() string { return e.message }

func cycleEvents() []event {
	err := errors.New("configuration is unavailable")
	criticalErr := errors.New("service cannot continue: required configuration is invalid")
	metricsErr := errors.New("request failed: monitored by the request error-rate metric")
	wrappedErr := fmt.Errorf("query failed: %w", databaseError{"connection refused"})
	wrappedErr = fmt.Errorf("request failed: %w", wrappedErr)
	return []event{
		{Level: slog.LevelInfo, Message: "request completed", Scenario: "info"},
		{Level: slog.LevelError, Message: err.Error(), Scenario: "errors-new", Err: err},
		{Level: slog.LevelError, Message: wrappedErr.Error(), Scenario: "nested-wrapped", Err: wrappedErr},
		{Level: levelCritical, Message: criticalErr.Error(), Scenario: "critical", Err: criticalErr},
		{Level: slog.LevelError, Message: metricsErr.Error(), Scenario: "metrics-monitored", Err: metricsErr, ExcludeFromGeneralRule: true},
	}
}

func databaseFailure(ctx context.Context, e *emitter, n int) {
	err := databaseError{fmt.Sprintf("database request %d timed out", 100+n)}
	e.Emit(ctx, event{Level: slog.LevelError, Message: err.Error(), Scenario: "database", Err: err})
}

func paymentFailure(ctx context.Context, e *emitter, n int) {
	err := paymentError{fmt.Sprintf("payment request %d timed out", 100+n)}
	e.Emit(ctx, event{Level: slog.LevelError, Message: err.Error(), Scenario: "payment", Err: err, RunbookURL: "https://example.com/runbooks/payment"})
}

func generateLogs(ctx context.Context, e *emitter, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for n := 0; ; n++ {
		if ctx.Err() != nil {
			return nil
		}
		for _, ev := range cycleEvents() {
			e.Emit(ctx, ev)
		}
		databaseFailure(ctx, e, n)
		paymentFailure(ctx, e, n)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
