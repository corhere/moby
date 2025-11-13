package otelutil

import (
	"context"
	"io"
	"os"

	"github.com/containerd/log"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/moby/moby/v2/daemon/internal/otelutil/otlptracefile"
	"github.com/moby/moby/v2/pkg/ioutils"
	"go.opentelemetry.io/contrib/processors/baggagecopy"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func init() {
	detect.Register("otlp/file", detect.TraceExporterDetector(func() (sdktrace.SpanExporter, error) {
		if os.Getenv("OTEL_TRACES_EXPORTER") != "otlp/file" {
			return nil, nil
		}
		var dest io.WriteCloser
		path := os.Getenv("OTEL_EXPORTER_OTLP_FILE_PATH")
		if path == "" {
			dest = ioutils.NewWriteCloserWrapper(os.Stdout, func() error { return nil })
		} else {
			var err error
			dest, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
		}
		return otlptracefile.New(context.Background(), dest)
	}), 1000)
}

func NewTracerProvider(ctx context.Context, allowNoop bool) (trace.TracerProvider, func(context.Context) error) {
	noopShutdown := func(ctx context.Context) error { return nil }

	exp, err := detect.NewSpanExporter(ctx)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to initialize tracing, skipping")
		if allowNoop {
			return noop.NewTracerProvider(), noopShutdown
		}
	}

	if allowNoop && detect.IsNoneSpanExporter(exp) {
		log.G(ctx).Info("OTEL tracing is not configured, using no-op tracer provider")
		return noop.NewTracerProvider(), noopShutdown
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.Default()),
		sdktrace.WithSyncer(detect.Recorder),
		sdktrace.WithBatcher(exp),
		sdktrace.WithSpanProcessor(baggagecopy.NewSpanProcessor(func(member baggage.Member) bool { return true })),
	)
	return tp, tp.Shutdown
}
