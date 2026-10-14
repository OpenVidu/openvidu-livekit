// Copyright 2026 OpenVidu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// Entity labels of the reconciler metrics.
const (
	entityRoom           = "room"
	entityParticipant    = "participant"
	entityTrack          = "track"
	entitySubscription   = "subscription"
	entityEgress         = "egress"
	entityIngressSession = "ingress_session"
)

// reconcilerMetrics are the reconciler's metrics on the server's Prometheus endpoint.
type reconcilerMetrics struct {
	runs        *prometheus.CounterVec
	closed      *prometheus.CounterVec
	candidates  *prometheus.GaugeVec
	runDuration prometheus.Histogram
}

var (
	registerReconcilerOnce      sync.Once
	registeredReconcilerMetrics *reconcilerMetrics
)

// newReconcilerMetrics registers the reconciler metrics on the default registry once and returns them.
func newReconcilerMetrics() *reconcilerMetrics {
	registerReconcilerOnce.Do(func() {
		registeredReconcilerMetrics = buildReconcilerMetrics()
		prometheus.MustRegister(registeredReconcilerMetrics.runs, registeredReconcilerMetrics.closed,
			registeredReconcilerMetrics.candidates, registeredReconcilerMetrics.runDuration)
	})
	return registeredReconcilerMetrics
}

func buildReconcilerMetrics() *reconcilerMetrics {
	return &reconcilerMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_reconciler_runs_total",
			Help: "Reconciler runs by result (ok, partial, live-state-error, live-state-untrusted, error).",
		}, []string{"result"}),
		closed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_reconciler_closed_total", Help: "Entities closed by the reconciler by type.",
		}, []string{"entity"}),
		candidates: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openvidu_analytics_reconciler_candidates", Help: "Candidates of the last reconciler run by type.",
		}, []string{"entity"}),
		runDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openvidu_analytics_reconciler_run_duration_seconds", Help: "Duration of a reconciler run.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
	}
}

func (m *reconcilerMetrics) observe(report runReport) {
	m.runs.WithLabelValues(report.result).Inc()
	for entity, value := range map[string]int32{
		entityRoom: report.candidates.Room, entityParticipant: report.candidates.Participant,
		entityEgress: report.candidates.Egress, entityIngressSession: report.candidates.IngressSession,
	} {
		m.candidates.WithLabelValues(entity).Set(float64(value))
	}
	for entity, value := range closedByEntity(report.closed) {
		if value > 0 {
			m.closed.WithLabelValues(entity).Add(float64(value))
		}
	}
	m.runDuration.Observe(report.duration.Seconds())
}

func closedByEntity(closed schema.ReconcilerClosures) map[string]int32 {
	return map[string]int32{
		entityRoom: closed.Room, entityParticipant: closed.Participant, entityTrack: closed.Track,
		entitySubscription: closed.Subscription, entityEgress: closed.Egress, entityIngressSession: closed.IngressSession,
	}
}
