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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// fakeStore records what batches write; failures and delays are configurable.
type fakeStore struct {
	mu          sync.Mutex
	delay       time.Duration
	failures    int
	failWith    error
	calls       atomic.Int32
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	events      []string
	stats       int
	increments  int
	heartbeats  int
}

func (f *fakeStore) enter() func() {
	current := f.inFlight.Add(1)
	for {
		highest := f.maxInFlight.Load()
		if current <= highest || f.maxInFlight.CompareAndSwap(highest, current) {
			break
		}
	}
	return func() { f.inFlight.Add(-1) }
}

func (f *fakeStore) fail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return f.failWith
	}
	return nil
}

func (f *fakeStore) ApplyUpdates(context.Context, []projection.Update) error {
	f.calls.Add(1)
	defer f.enter()()
	time.Sleep(f.delay)
	return f.fail()
}

func (f *fakeStore) ApplyCascades(context.Context, []projection.Update) error { return nil }

func (f *fakeStore) ApplyIncrements(_ context.Context, increments []projection.Increment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.increments += len(increments)
	return nil
}

func (f *fakeStore) InsertEvents(_ context.Context, events []schema.EventDocument) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inserted := map[string]bool{}
	for _, event := range events {
		f.events = append(f.events, event.ID)
		inserted[event.ID] = true
	}
	return inserted, nil
}

func (f *fakeStore) InsertStats(_ context.Context, stats []schema.TrackStatsDocument) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats += len(stats)
	return nil
}

func (f *fakeStore) ParticipantSessions(context.Context, []string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (f *fakeStore) Identities(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeStore) Tracks(context.Context, []string, []string) (map[string]projection.TrackRef, map[string][]string, error) {
	return map[string]projection.TrackRef{}, map[string][]string{}, nil
}

func (f *fakeStore) Heartbeat(context.Context, string, string, string, time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats++
	return nil
}

func (f *fakeStore) RoundTrips() int  { return 0 }
func (f *fakeStore) ResetRoundTrips() {}

func (f *fakeStore) writtenEvents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func testSettings(t *testing.T, configure func(*openviduconfig.AnalyticsConfig)) openviduconfig.AnalyticsSettings {
	conf := openviduconfig.AnalyticsConfig{Enabled: true, MongoUrl: "mongodb://test"}
	if configure != nil {
		configure(&conf)
	}
	settings, _, err := conf.Settings()
	require.NoError(t, err)
	return settings
}

func roomEvents(n int) []*livekit.AnalyticsEvent {
	var ev events
	batch := make([]*livekit.AnalyticsEvent, n)
	for i := range batch {
		batch[i] = ev.roomCreated(fmt.Sprintf("RM_%d", i), "room", at("0s"))
	}
	return batch
}

func counterValue(vec *prometheus.CounterVec, labels ...string) float64 {
	return testutil.ToFloat64(vec.WithLabelValues(labels...))
}

func TestOverflowDropsOldestAndCounts(t *testing.T) {
	settings := testSettings(t, nil)
	settings.MaxEvents = 100
	w := newWriter(&fakeStore{}, settings, Node{ID: testNode}, newMetrics())
	before := counterValue(w.metrics.dropped, "events", "overflow")

	w.enqueueEvents(roomEvents(150))

	require.Equal(t, 100, w.events.Len())
	require.Equal(t, float64(50), counterValue(w.metrics.dropped, "events", "overflow")-before)
	require.Equal(t, "AE_ROOM_CREATED_0051", w.events.Drain(1)[0].event.GetId(), "the latest 100 events are kept")
}

func TestOneBatchInFlightAndSendNeverBlocks(t *testing.T) {
	store := &fakeStore{delay: 200 * time.Millisecond}
	settings := testSettings(t, nil)
	settings.Interval = 10 * time.Millisecond
	w := newWriter(store, settings, Node{ID: testNode}, newMetrics())
	go w.run()

	var slowest time.Duration
	for i := 0; i < 20; i++ {
		started := time.Now()
		w.enqueueEvents(roomEvents(10))
		slowest = max(slowest, time.Since(started))
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.stop(ctx)

	require.Len(t, store.writtenEvents(), 200, "stop flushes what is queued")
	require.EqualValues(t, 1, store.maxInFlight.Load(), "batches never overlap")
	require.Less(t, slowest, 50*time.Millisecond, "enqueueing does not wait for the database")
}

func TestTransientErrorRetriesTheSameBatchFirst(t *testing.T) {
	store := &fakeStore{failures: 2, failWith: mongo.CommandError{Code: 10107, Message: "not primary"}}
	w := newWriter(store, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	before := counterValue(w.metrics.dropped, "events", "error")
	first := roomEvents(3)
	w.enqueueEvents(first)

	require.Error(t, w.tick(context.Background()))
	var ev events
	ev.sequence = 100
	later := []*livekit.AnalyticsEvent{ev.roomCreated("RM_late", "late", at("1s"))}
	w.enqueueEvents(later)
	require.Error(t, w.tick(context.Background()))
	require.NoError(t, w.tick(context.Background()))

	written := store.writtenEvents()
	require.Equal(t, []string{first[0].GetId(), first[1].GetId(), first[2].GetId(), later[0].GetId()}, written)
	require.Equal(t, before, counterValue(w.metrics.dropped, "events", "error"))
}

func TestNonTransientErrorDropsTheBatch(t *testing.T) {
	store := &fakeStore{failures: 1, failWith: fmt.Errorf("cannot encode")}
	w := newWriter(store, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	before := counterValue(w.metrics.dropped, "events", "error")
	w.enqueueEvents(roomEvents(2))

	require.Error(t, w.tick(context.Background()))
	require.Zero(t, w.events.Len())
	require.Equal(t, float64(2), counterValue(w.metrics.dropped, "events", "error")-before)
}

func TestUnsupportedEventsAreCounted(t *testing.T) {
	w := newWriter(&fakeStore{}, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	before := counterValue(w.metrics.dropped, "events", "unsupported")
	var ev events
	w.enqueueEvents([]*livekit.AnalyticsEvent{ev.next(livekit.AnalyticsEventType_SIP_CALL_STARTED, at("0s"))})

	require.NoError(t, w.tick(context.Background()))
	require.Equal(t, float64(1), counterValue(w.metrics.dropped, "events", "unsupported")-before)
}

func TestStopFlushesQueuesAndBoundsTheWait(t *testing.T) {
	store := &fakeStore{}
	settings := testSettings(t, nil)
	settings.Interval = time.Hour
	w := newWriter(store, settings, Node{ID: testNode}, newMetrics())
	go w.run()
	w.enqueueEvents(roomEvents(5))
	w.enqueueStats([]*livekit.AnalyticsStat{stat("RM_0", "PA_a", "TR_a", livekit.StreamType_UPSTREAM, at("1m"), 4, 100)})

	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	w.stop(ctx)
	require.Len(t, store.writtenEvents(), 5)
	require.NotZero(t, store.stats)

	blocked := &fakeStore{failures: 1 << 30, failWith: mongo.CommandError{Code: 6, Message: "host unreachable"}}
	stuck := newWriter(blocked, settings, Node{ID: testNode}, newMetrics())
	droppedBefore := counterValue(stuck.metrics.dropped, "events", "shutdown")
	go stuck.run()
	stuck.enqueueEvents(roomEvents(5))
	started := time.Now()
	short, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelShort()
	stuck.stop(short)
	require.Less(t, time.Since(started), time.Second, "an unreachable database does not delay the exit beyond the deadline")
	<-stuck.stopped
	require.Equal(t, float64(5), counterValue(stuck.metrics.dropped, "events", "shutdown")-droppedBefore)
	require.Less(t, blocked.calls.Load(), int32(10), "retries during shutdown are spaced")
}

func TestHeartbeatOnIdleTicks(t *testing.T) {
	store := &fakeStore{}
	w := newWriter(store, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	require.NoError(t, w.tick(context.Background()))
	require.NoError(t, w.tick(context.Background()))
	require.Equal(t, 2, store.heartbeats)
}

func TestRateLimitedLog(t *testing.T) {
	var lines []string
	log := newRateLimitedLog(10 * time.Second)
	now := time.Unix(0, 0)
	log.now = func() time.Time { return now }
	log.emit = func(message string, fields ...any) {
		lines = append(lines, fmt.Sprint(append([]any{message}, fields...)...))
	}

	for i := 0; i < 6; i++ {
		log.warn("retry", "analytics: retrying", "queued", i)
		log.warn("rejected:rooms", "analytics: rejected", "count", 1)
		now = now.Add(2 * time.Second)
	}
	require.Len(t, lines, 4, "one line per cause per interval")
	require.Contains(t, lines[2], "suppressed")
}

func TestMetricFamiliesAfterABatch(t *testing.T) {
	w := newWriter(&fakeStore{}, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	w.enqueueEvents(roomEvents(1))
	require.NoError(t, w.tick(context.Background()))
	w.metrics.dropped.WithLabelValues("events", "overflow").Add(0)
	w.metrics.writeErrors.WithLabelValues("rooms", "rejected").Add(0)

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	labels := map[string]map[string]bool{}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "openvidu_analytics_") {
			continue
		}
		labels[family.GetName()] = map[string]bool{}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				labels[family.GetName()][label.GetName()] = true
			}
		}
	}
	require.Equal(t, map[string]map[string]bool{
		"openvidu_analytics_queue_depth":            {"queue": true},
		"openvidu_analytics_batches_total":          {"result": true},
		"openvidu_analytics_batch_duration_seconds": {},
		"openvidu_analytics_dropped_total":          {"queue": true, "reason": true},
		"openvidu_analytics_write_errors_total":     {"collection": true, "kind": true},
	}, labels)
}
