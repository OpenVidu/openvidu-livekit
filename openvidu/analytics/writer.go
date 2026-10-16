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
	"context"
	"errors"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/openvidu/openviduversion"
	"github.com/livekit/livekit-server/openvidu/queue"
)

// drainRetryDelay spaces the retries of the shutdown flush when the database fails fast (for example, no primary).
const drainRetryDelay = 250 * time.Millisecond

// writer runs one batch at a time every interval: it drains the event and stats queues, applies them and refreshes
// the node heartbeat. A batch that fails with a transient error keeps its entries and the steps already applied,
// and is written first in the next batch.
type writer struct {
	processor *processor
	store     batchStore
	interval  time.Duration
	node      Node
	metrics   *metrics
	logs      *rateLimitedLog

	events *queue.Ring[*eventEntry]
	stats  *queue.Ring[*livekit.AnalyticsStat]
	// pendingStats are stats batches interrupted by a transient error, retried before new stats.
	pendingStats []*statsEntry

	stopOnce sync.Once
	stopping chan struct{}
	deadline chan context.Context
	stopped  chan struct{}
}

func newWriter(s batchStore, settings openviduconfig.AnalyticsSettings, node Node, m *metrics) *writer {
	w := &writer{
		store:    s,
		interval: settings.Interval,
		node:     node,
		metrics:  m,
		logs:     newRateLimitedLog(settings.Interval),
		events:   queue.NewRing[*eventEntry](settings.MaxEvents),
		stats:    queue.NewRing[*livekit.AnalyticsStat](settings.MaxStats),
		stopping: make(chan struct{}),
		deadline: make(chan context.Context, 1),
		stopped:  make(chan struct{}),
	}
	w.processor = &processor{
		store: s,
		opts:  projectionOptions(settings),
		cache: newRefCache(),
		onWriteErrors: func(err *store.WriteError) {
			m.writeErrors.WithLabelValues(err.Collection, "rejected").Add(float64(err.Count))
			w.logs.warn("rejected:"+err.Collection, "analytics: writes rejected by MongoDB and dropped",
				"collection", err.Collection, "count", err.Count, "error", err.Err.Error())
		},
		onUnsupported: func(event *livekit.AnalyticsEvent) {
			m.dropped.WithLabelValues("events", "unsupported").Inc()
			w.logs.warn("unsupported", "analytics: event type not projected", "type", event.GetType().String())
		},
	}
	return w
}

func (w *writer) enqueueEvents(events []*livekit.AnalyticsEvent) {
	for _, event := range events {
		if _, dropped := w.events.Push(&eventEntry{event: event}); dropped {
			w.metrics.dropped.WithLabelValues("events", "overflow").Inc()
		}
	}
	w.metrics.queueDepth.WithLabelValues("events").Set(float64(w.events.Len()))
}

func (w *writer) enqueueStats(stats []*livekit.AnalyticsStat) {
	for _, stat := range stats {
		if _, dropped := w.stats.Push(stat); dropped {
			w.metrics.dropped.WithLabelValues("stats", "overflow").Inc()
		}
	}
	w.metrics.queueDepth.WithLabelValues("stats").Set(float64(w.stats.Len()))
}

func (w *writer) run() {
	defer close(w.stopped)
	timer := time.NewTimer(w.interval)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			started := time.Now()
			w.tick(context.Background())
			timer.Reset(max(w.interval-time.Since(started), 0))
		case <-w.stopping:
			w.drain(<-w.deadline)
			return
		}
	}
}

// tick applies one batch and refreshes the heartbeat.
func (w *writer) tick(ctx context.Context) error {
	started := time.Now()
	events := w.events.Drain(0)
	stats := w.pendingStats
	w.pendingStats = nil
	if drained := w.stats.Drain(0); len(drained) > 0 {
		stats = append(stats, &statsEntry{stats: drained})
	}
	return w.applyBatch(ctx, events, stats, started)
}

func (w *writer) applyBatch(ctx context.Context, events []*eventEntry, stats []*statsEntry, started time.Time) error {
	w.store.ResetRoundTrips()
	err := w.processor.apply(ctx, events, stats)
	if heartbeatErr := w.store.Heartbeat(ctx, w.node.ID, openviduversion.Version, w.node.Region, time.Now().UTC().Truncate(time.Millisecond)); err == nil {
		err = heartbeatErr
	}
	w.metrics.batchDuration.Observe(time.Since(started).Seconds())

	if err == nil {
		w.metrics.batches.WithLabelValues("success").Inc()
		logger.Debugw("analytics batch written", "events", len(events), "statsFlushes", len(stats),
			"roundTrips", w.store.RoundTrips(), "duration", time.Since(started))
		w.updateDepth()
		return nil
	}

	if !isTransient(err) {
		w.metrics.batches.WithLabelValues("failed").Inc()
		w.metrics.writeErrors.WithLabelValues("batch", "error").Inc()
		w.logs.warn("failed", "analytics: batch failed with a non-transient error and was dropped",
			"error", err.Error(), "events", len(events))
		w.metrics.dropped.WithLabelValues("events", "error").Add(float64(countPending(events)))
		w.updateDepth()
		return err
	}

	w.metrics.batches.WithLabelValues("retry").Inc()
	var remaining []*eventEntry
	for _, entry := range events {
		if !entry.done() {
			remaining = append(remaining, entry)
		}
	}
	if dropped := w.events.Requeue(remaining); dropped > 0 {
		w.metrics.dropped.WithLabelValues("events", "overflow").Add(float64(dropped))
	}
	for _, entry := range stats {
		if !entry.done() {
			w.pendingStats = append(w.pendingStats, entry)
		}
	}
	w.logs.warn("retry", "analytics: batch failed with a transient error, retrying in the next interval",
		"error", err.Error(), "eventsQueued", w.events.Len(), "statsQueued", w.stats.Len())
	w.updateDepth()
	return err
}

func countPending(events []*eventEntry) int {
	count := 0
	for _, entry := range events {
		if !entry.done() {
			count++
		}
	}
	return count
}

func (w *writer) updateDepth() {
	w.metrics.queueDepth.WithLabelValues("events").Set(float64(w.events.Len()))
	w.metrics.queueDepth.WithLabelValues("stats").Set(float64(w.stats.Len()))
}

// stop asks the loop to flush and waits for it, at most until ctx expires.
func (w *writer) stop(ctx context.Context) {
	w.stopOnce.Do(func() {
		w.deadline <- ctx
		close(w.stopping)
	})
	select {
	case <-w.stopped:
	case <-ctx.Done():
	}
}

// drain writes batches until the queues are empty or ctx expires, then counts what is left as dropped.
func (w *writer) drain(ctx context.Context) {
	for w.events.Len() > 0 || w.stats.Len() > 0 || len(w.pendingStats) > 0 {
		if ctx.Err() != nil {
			break
		}
		err := w.tick(ctx)
		if err != nil && !isTransient(err) {
			break
		}
		if err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(drainRetryDelay):
			}
		}
	}
	events, stats := w.events.Len(), w.stats.Len()
	if events+stats > 0 {
		w.metrics.dropped.WithLabelValues("events", "shutdown").Add(float64(events))
		w.metrics.dropped.WithLabelValues("stats", "shutdown").Add(float64(stats))
		logger.Warnw("analytics: entries not written before shutdown", nil, "events", events, "stats", stats)
		return
	}
	logger.Infow("analytics queues flushed")
}

// isTransient reports errors a later retry can overcome: network errors, timeouts, elections and write concern.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if mongo.IsNetworkError(err) || mongo.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var serverErr mongo.ServerError
	if errors.As(err, &serverErr) {
		if serverErr.HasErrorLabel("RetryableWriteError") || serverErr.HasErrorLabel("TransientTransactionError") {
			return true
		}
		for _, code := range []int{6, 7, 89, 91, 189, 262, 9001, 10107, 11600, 11602, 13435, 13436, 64} {
			if serverErr.HasErrorCode(code) {
				return true
			}
		}
	}
	var bulkErr mongo.BulkWriteException
	if errors.As(err, &bulkErr) && bulkErr.WriteConcernError != nil {
		return true
	}
	return false
}
