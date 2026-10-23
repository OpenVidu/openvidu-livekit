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
)

// migrationStateValues are the values of the migration_state gauge.
var migrationStateValues = map[string]float64{
	"":                 0,
	migrationPending:   1,
	migrationRunning:   2,
	migrationCompleted: 3,
	migrationFailed:    4,
}

// migrationMetrics are the legacy migration's metrics on the server's Prometheus endpoint.
type migrationMetrics struct {
	state         prometheus.Gauge
	processed     *prometheus.CounterVec
	batchDuration prometheus.Histogram
}

var (
	registerMigrationOnce      sync.Once
	registeredMigrationMetrics *migrationMetrics
)

// newMigrationMetrics registers the migration metrics on the default registry once and returns them.
func newMigrationMetrics() *migrationMetrics {
	registerMigrationOnce.Do(func() {
		registeredMigrationMetrics = buildMigrationMetrics()
		prometheus.MustRegister(registeredMigrationMetrics.state, registeredMigrationMetrics.processed,
			registeredMigrationMetrics.batchDuration)
	})
	return registeredMigrationMetrics
}

func buildMigrationMetrics() *migrationMetrics {
	return &migrationMetrics{
		state: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openvidu_analytics_migration_state",
			Help: "Legacy data migration state: 0 none, 1 pending, 2 running, 3 completed, 4 failed.",
		}),
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_migration_processed_total", Help: "Legacy documents replayed by collection.",
		}, []string{"collection"}),
		batchDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openvidu_analytics_migration_batch_seconds", Help: "Duration of a legacy migration batch.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		}),
	}
}

func (m *migrationMetrics) setState(state string) {
	m.state.Set(migrationStateValues[state])
}
