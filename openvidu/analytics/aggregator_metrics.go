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
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// aggregatorMetrics are the aggregator's metrics on the server's Prometheus endpoint.
type aggregatorMetrics struct {
	runs        *prometheus.CounterVec
	runDuration prometheus.Histogram
}

var (
	registerAggregatorOnce      sync.Once
	registeredAggregatorMetrics *aggregatorMetrics
)

// newAggregatorMetrics registers the aggregator metrics on the default registry once and returns them.
func newAggregatorMetrics() *aggregatorMetrics {
	registerAggregatorOnce.Do(func() {
		registeredAggregatorMetrics = buildAggregatorMetrics()
		prometheus.MustRegister(registeredAggregatorMetrics.runs, registeredAggregatorMetrics.runDuration)
	})
	return registeredAggregatorMetrics
}

func buildAggregatorMetrics() *aggregatorMetrics {
	return &aggregatorMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_aggregator_runs_total", Help: "Aggregator runs by result (ok, error).",
		}, []string{"result"}),
		runDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openvidu_analytics_aggregator_run_duration_seconds", Help: "Duration of an aggregator run.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),
	}
}

func (m *aggregatorMetrics) observe(err error, duration time.Duration) {
	result := resultOK
	if err != nil {
		result = resultError
	}
	m.runs.WithLabelValues(result).Inc()
	m.runDuration.Observe(duration.Seconds())
}
