package metrics

import (
	opmetrics "github.com/ethereum-optimism/optimism/op-service/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

const opPlasmaClientSubsystem = "op_plasma_client"

type PlasmaClientMetricer interface {
	RecordDAServerPutRequestSubmitted()
	RecordDAServerPutResponseReceived()

	Document() []opmetrics.DocumentedMetric
}

type PlasmaClientMetrics struct {
	registry            *prometheus.Registry
	factory             opmetrics.Factory
	pendingDaServerPuts prometheus.Gauge
}

var _ PlasmaClientMetricer = (*PlasmaClientMetrics)(nil)

// implements the Registry getter, for metrics HTTP server to hook into
var _ opmetrics.RegistryMetricer = (*PlasmaClientMetrics)(nil)

func NewMetrics(namespace string) PlasmaClientMetrics {
	if namespace == "" {
		namespace = "default"
	}

	registry := opmetrics.NewRegistry()
	factory := opmetrics.With(registry)

	return PlasmaClientMetrics{
		registry: registry,
		factory:  factory,

		pendingDaServerPuts: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: opPlasmaClientSubsystem,
			Name:      "pending_da_server_puts",
			Help:      "Number of pending puts to the DA server.",
		}),
	}
}

func (m *PlasmaClientMetrics) Registry() *prometheus.Registry {
	return m.registry
}

func (m *PlasmaClientMetrics) Document() []opmetrics.DocumentedMetric {
	return m.factory.Document()
}

func (m *PlasmaClientMetrics) RecordDAServerPutRequestSubmitted() {
	m.pendingDaServerPuts.Inc()
}

func (m *PlasmaClientMetrics) RecordDAServerPutResponseReceived() {
	m.pendingDaServerPuts.Dec()
}

type NoopPlasmaClientMetrics struct{}

var _ PlasmaClientMetricer = (*NoopPlasmaClientMetrics)(nil)

func (*NoopPlasmaClientMetrics) Document() []opmetrics.DocumentedMetric {
	return nil
}

func (*NoopPlasmaClientMetrics) RecordDAServerPutRequestSubmitted() {}

func (*NoopPlasmaClientMetrics) RecordDAServerPutResponseReceived() {}
