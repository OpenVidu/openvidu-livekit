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

	"github.com/livekit/protocol/logger"
	"github.com/prometheus/client_golang/prometheus"
)

// metrics of the write path, exposed on the server's Prometheus endpoint.
type metrics struct {
	queueDepth    *prometheus.GaugeVec
	batches       *prometheus.CounterVec
	batchDuration prometheus.Histogram
	dropped       *prometheus.CounterVec
	writeErrors   *prometheus.CounterVec
}

var (
	registerOnce      sync.Once
	registeredMetrics *metrics
)

// newMetrics registers the metrics on the default registry once and returns them.
func newMetrics() *metrics {
	registerOnce.Do(func() {
		registeredMetrics = buildMetrics()
		prometheus.MustRegister(registeredMetrics.queueDepth, registeredMetrics.batches, registeredMetrics.batchDuration,
			registeredMetrics.dropped, registeredMetrics.writeErrors)
	})
	return registeredMetrics
}

func buildMetrics() *metrics {
	return &metrics{
		queueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openvidu_analytics_queue_depth", Help: "Entries waiting in the analytics queues.",
		}, []string{"queue"}),
		batches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_batches_total", Help: "Analytics batches by result (success, retry, failed).",
		}, []string{"result"}),
		batchDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openvidu_analytics_batch_duration_seconds", Help: "Duration of an analytics batch.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
		}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_dropped_total", Help: "Analytics entries dropped by queue and reason.",
		}, []string{"queue", "reason"}),
		writeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openvidu_analytics_write_errors_total", Help: "Analytics writes rejected by collection and kind.",
		}, []string{"collection", "kind"}),
	}
}

// rateLimitedLog writes at most one warning per cause and interval, counting the ones it suppresses.
type rateLimitedLog struct {
	mu         sync.Mutex
	interval   time.Duration
	last       map[string]time.Time
	suppressed map[string]int
	now        func() time.Time
	emit       func(message string, fields ...any)
}

func newRateLimitedLog(interval time.Duration) *rateLimitedLog {
	return &rateLimitedLog{
		interval: interval, last: map[string]time.Time{}, suppressed: map[string]int{}, now: time.Now,
		emit: func(message string, fields ...any) { logger.Warnw(message, nil, fields...) },
	}
}

// warn writes the warning unless another one of the same cause was written within the interval; it reports whether it
// wrote it.
func (l *rateLimitedLog) warn(cause string, message string, fields ...any) bool {
	l.mu.Lock()
	now := l.now()
	if last, ok := l.last[cause]; ok && now.Sub(last) < l.interval {
		l.suppressed[cause]++
		l.mu.Unlock()
		return false
	}
	suppressed := l.suppressed[cause]
	l.last[cause] = now
	l.suppressed[cause] = 0
	l.mu.Unlock()
	if suppressed > 0 {
		fields = append(fields, "suppressed", suppressed)
	}
	l.emit(message, fields...)
	return true
}
