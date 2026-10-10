package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/errors/errbase"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type config struct {
	Environment string
}

func loadConfig() config {
	return config{
		Environment: envOr("DEPLOYMENT_ENVIRONMENT", "development"),
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newProvider(ctx context.Context, cfg config) (*sdklog.LoggerProvider, error) {
	exporter, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("otel-log-demo"),
		semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
	)
	return sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter))), nil
}

type emitter struct {
	logger *slog.Logger
}

// Use the concrete error type without a leading '*', similar to Rollbar.
// Wrappers retain their own type; do not unwrap to the root cause.
func exceptionType(err error) string {
	if err == nil {
		return "<nil>"
	}
	class := reflect.TypeOf(err).String()
	if class == "" {
		return "panic"
	}
	return strings.TrimPrefix(class, "*")
}

// Preserve formatted error-chain details, including through standard %w wrappers.
func exceptionStacktrace(err error) string {
	if err == nil {
		return ""
	}
	// Capture at the caller only when the error chain has no stack provider.
	type stackError interface {
		error
		errbase.StackTraceProvider
	}
	var serr stackError
	if !errors.As(err, &serr) {
		err = errors.WithStackDepth(err, 1)
	}
	return fmt.Sprintf("%+v", errors.Formattable(err))
}

func (e *emitter) Emit(ctx context.Context, ev event) {
	attrs := []slog.Attr{
		slog.String("demo.scenario", ev.Scenario),
		slog.Bool("alert.general_rule.exclude", ev.ExcludeFromGeneralRule),
	}
	if ev.Err != nil {
		attrs = append(attrs,
			slog.String("exception.type", exceptionType(ev.Err)),
			slog.String("exception.message", ev.Err.Error()),
			slog.String("exception.stacktrace", exceptionStacktrace(ev.Err)),
		)
	}
	if ev.RunbookURL != "" {
		attrs = append(attrs, slog.String("runbook_url", ev.RunbookURL))
	}
	e.logger.LogAttrs(ctx, ev.Level, ev.Message, attrs...)
}

func run(ctx context.Context) error {
	cfg := loadConfig()
	provider, err := newProvider(ctx, cfg)
	if err != nil {
		return fmt.Errorf("initialize OTLP exporter: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			log.Printf("OTLP shutdown: %v", err)
		}
	}()
	e := &emitter{logger: otelslog.NewLogger("otel-log-demo", otelslog.WithLoggerProvider(provider))}
	log.Printf("Sending demo logs (environment=%s); Ctrl-C to stop", cfg.Environment)
	return generateLogs(ctx, e, 5*time.Second)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
