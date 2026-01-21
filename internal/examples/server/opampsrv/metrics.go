package opampsrv

import (
	"context"
	"io"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	otelresource "go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

var (
	opampReadErrAttr = attribute.String("error.type", "read")
	opampRespErrAttr = attribute.String("error.type", "resp")
)

// metricsTracker is a struct to encasulate all metrics the OpAMP server tracks.
type metricsTracker struct {
	connCounter metric.Int64UpDownCounter
	errCounter  metric.Int64Counter
	meter       metric.Meter

	resource *otelresource.Resource
	exporter sdkmetric.Exporter
}

func NewMetricsTracker(emitMetrics bool) (*metricsTracker, error) {
	resource, err := otelresource.New(context.Background(),
		otelresource.WithAttributes(
			semconv.ServiceNameKey.String("io.opentelemetry.opampserver"),
			semconv.ServiceVersionKey.String("0.1.0"),
		),
	)
	if err != nil {
		return nil, err
	}
	exporterOpts := []stdoutmetric.Option{stdoutmetric.WithPrettyPrint()}
	if !emitMetrics {
		exporterOpts = append(exporterOpts, stdoutmetric.WithWriter(io.Discard))
	}

	exporter, err := stdoutmetric.New(exporterOpts...)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	otel.SetMeterProvider(meterProvider)
	meter := otel.Meter("opamp")
	connCounter, err := meter.Int64UpDownCounter("connections.active.count")
	if err != nil {
		return nil, err
	}

	errCounter, err := meter.Int64Counter("connections.error.count")
	if err != nil {
		return nil, err
	}

	return &metricsTracker{
		connCounter: connCounter,
		errCounter:  errCounter,
		meter:       meter,
		resource:    resource,
		exporter:    exporter,
	}, nil
}

func (m *metricsTracker) OnConnected(ctx context.Context) {
	m.connCounter.Add(ctx, 1)
}

func (m *metricsTracker) OnDisconnect(ctx context.Context) {
	m.connCounter.Add(ctx, -1)
}

func (m *metricsTracker) OnReadMessageError(ctx context.Context) {
	m.errCounter.Add(ctx, 1, metric.WithAttributes(opampReadErrAttr))
}

func (m *metricsTracker) OnMessageResponseError(ctx context.Context) {
	m.errCounter.Add(ctx, 1, metric.WithAttributes(opampRespErrAttr))
}
