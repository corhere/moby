// Package otlptracefile provides a simple implementation of the draft
// OpenTelemetry Protocol File Exporter.
//
// https://opentelemetry.io/docs/specs/otel/protocol/file-exporter/
package otlptracefile

import (
	"context"
	"io"
	"sync"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type client struct {
	dest     io.WriteCloser
	uploadMu sync.RWMutex
}

// Start implements otlptrace.Client.
func (c *client) Start(ctx context.Context) error {
	return nil
}

// Stop implements otlptrace.Client.
func (c *client) Stop(ctx context.Context) error {
	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()
	return c.dest.Close()
}

// UploadTraces implements otlptrace.Client.
func (c *client) UploadTraces(ctx context.Context, protoSpans []*tracepb.ResourceSpans) error {
	c.uploadMu.RLock()
	defer c.uploadMu.RUnlock()
	pb := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: protoSpans,
	}
	m, err := proto.Marshal(pb)
	if err != nil {
		return err
	}
	tr, err := ((*ptrace.ProtoUnmarshaler)(nil)).UnmarshalTraces(m)
	if err != nil {
		return err
	}
	b, err := ptraceotlp.NewExportRequestFromTraces(tr).MarshalJSON()
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.dest.Write(b)
	return err
}

func NewClient(dest io.WriteCloser) otlptrace.Client {
	return &client{dest: dest}
}

func New(ctx context.Context, dest io.WriteCloser) (*otlptrace.Exporter, error) {
	return otlptrace.New(ctx, NewClient(dest))
}

func NewUnstarted(dest io.WriteCloser) *otlptrace.Exporter {
	return otlptrace.NewUnstarted(NewClient(dest))
}
