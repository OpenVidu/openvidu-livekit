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
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/pkg/config"
)

// fakeAggregatorStore answers the counts of the aggregator from memory and applies its increments to in-memory rollup
// documents: counters are added and maxima kept as MongoDB's $inc and $max do.
type fakeAggregatorStore struct {
	mu           sync.Mutex
	rooms        store.ActiveCount
	participants store.ActiveCount
	live         []string
	countErr     error
	documents    map[string]map[string]any
	increments   []projection.Increment
	pendingDays  []time.Time
	unique       map[time.Time]int32
	uniqueByNode map[time.Time]map[string]int32
	set          map[time.Time]int32
	running      *atomic.Int32
	overlaps     *atomic.Int32
}

func newFakeAggregatorStore() *fakeAggregatorStore {
	return &fakeAggregatorStore{documents: map[string]map[string]any{}, unique: map[time.Time]int32{},
		uniqueByNode: map[time.Time]map[string]int32{}, set: map[time.Time]int32{}}
}

// counts sets the active entities: rooms and STANDARD sessions per label, "" for those without one.
func (f *fakeAggregatorStore) counts(rooms map[string]int32, participants map[string]int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms, f.participants = activeCount(rooms), activeCount(participants)
}

func activeCount(byLabel map[string]int32) store.ActiveCount {
	count := store.ActiveCount{ByNode: map[string]int32{}}
	for label, n := range byLabel {
		count.Total += n
		if label != "" {
			count.ByNode[label] = n
		}
	}
	return count
}

func (f *fakeAggregatorStore) ActiveRooms(context.Context) (store.ActiveCount, error) {
	if f.running != nil && f.running.Add(1) > 1 {
		f.overlaps.Add(1)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rooms, f.countErr
}

func (f *fakeAggregatorStore) ActiveParticipants(context.Context) (store.ActiveCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.participants, nil
}

func (f *fakeAggregatorStore) LiveMediaNodes(context.Context, time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.live...), nil
}

func (f *fakeAggregatorStore) ApplyIncrements(_ context.Context, increments []projection.Increment) error {
	if f.running != nil {
		time.Sleep(5 * time.Millisecond)
		f.running.Add(-1)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.increments = append(f.increments, increments...)
	for _, increment := range increments {
		key := increment.Collection + "/" + increment.ID()
		document, ok := f.documents[key]
		if !ok {
			document = map[string]any{}
			f.documents[key] = document
		}
		for path, value := range increment.Counters {
			current, _ := document[path].(int32)
			document[path] = current + value.(int32)
		}
		for path, value := range increment.Maxima {
			if current, ok := document[path].(int32); !ok || value.(int32) > current {
				document[path] = value
			}
		}
		if increment.Reported {
			document["mediaStats.reported"] = true
		}
		if increment.Missing {
			document["mediaStats.missing"] = true
		}
	}
	return nil
}

func (f *fakeAggregatorStore) DaysWithoutUniqueIdentities(_ context.Context, from time.Time, to time.Time, limit int) ([]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var days []time.Time
	for _, day := range f.pendingDays {
		if _, done := f.set[day]; !done && !day.Before(from) && day.Before(to) && len(days) < limit {
			days = append(days, day)
		}
	}
	return days, nil
}

func (f *fakeAggregatorStore) UniqueIdentities(_ context.Context, from time.Time, _ time.Time) (int32, map[string]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unique[from], f.uniqueByNode[from], nil
}

func (f *fakeAggregatorStore) SetUniqueIdentities(_ context.Context, day time.Time, global int32, _ map[string]int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[day] = global
	return nil
}

// document returns the in-memory rollup document of a collection, scope and bucket.
func (f *fakeAggregatorStore) document(collection string, scope string, bucket time.Time) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.documents[collection+"/"+projection.Increment{Scope: scope, Bucket: bucket}.ID()]
}

func (f *fakeAggregatorStore) applied() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.increments)
}

func peaks(rooms int32, participants int32) map[string]any {
	return map[string]any{"rooms.peakActive": rooms, "participants.peakActive": participants}
}

func newTestAggregator(s aggregatorStore, at time.Time) (*aggregator, *capturedLog, *aggregatorMetrics) {
	m := buildAggregatorMetrics()
	a := newAggregator(s, aggregatorSettings{interval: time.Minute, entitiesRetention: 2160 * time.Hour}, m)
	logs := &capturedLog{}
	a.log = logs.sink()
	a.now = func() time.Time { return at }
	return a, logs, m
}

var (
	sampleDay  = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	sampleHour = sampleDay.Add(10 * time.Hour)
)

func TestAggregatorPeaks(t *testing.T) {
	ctx := context.Background()

	t.Run("busy minute", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"": 3}, map[string]int32{"": 7})
		a, _, _ := newTestAggregator(s, sampleHour.Add(2*time.Minute))
		require.NoError(t, a.run(ctx))
		for _, document := range []map[string]any{
			s.document(schema.CollectionAnalytics5Min, "global", sampleHour),
			s.document(schema.CollectionAnalyticsHourly, "global", sampleHour),
			s.document(schema.CollectionAnalyticsDaily, "global", sampleDay),
		} {
			require.Equal(t, peaks(3, 7), document)
		}
	})

	t.Run("peak kept across samples", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"": 4}, nil)
		a, _, _ := newTestAggregator(s, sampleHour.Add(time.Minute))
		require.NoError(t, a.run(ctx))
		s.counts(map[string]int32{"": 2}, nil)
		a.now = func() time.Time { return sampleHour.Add(3 * time.Minute) }
		require.NoError(t, a.run(ctx))
		require.Equal(t, int32(4), s.document(schema.CollectionAnalytics5Min, "global", sampleHour)["rooms.peakActive"])
		require.Equal(t, int32(4), s.document(schema.CollectionAnalyticsHourly, "global", sampleHour)["rooms.peakActive"])
	})

	t.Run("idle deployment", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(nil, nil)
		a, _, _ := newTestAggregator(s, sampleHour.Add(2*time.Minute))
		require.NoError(t, a.run(ctx))
		require.Equal(t, peaks(0, 0), s.document(schema.CollectionAnalytics5Min, "global", sampleHour),
			"the peaks and nothing else")
		require.Len(t, s.documents, 3)
	})

	t.Run("media statistics marks untouched", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"mn-1": 1}, map[string]int32{"mn-1": 2})
		s.live = []string{"mn-2"}
		a, _, _ := newTestAggregator(s, sampleHour)
		require.NoError(t, a.run(ctx))
		for _, increment := range s.increments {
			require.False(t, increment.Reported || increment.Missing, increment.ID())
			require.Empty(t, increment.Counters, increment.ID())
		}
	})

	t.Run("two media nodes", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"mn-1": 2, "mn-2": 1}, map[string]int32{"mn-1": 5, "mn-2": 2})
		a, _, _ := newTestAggregator(s, sampleHour)
		require.NoError(t, a.run(ctx))
		for _, collection := range []string{schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly} {
			require.Equal(t, peaks(3, 7), s.document(collection, "global", sampleHour))
			require.Equal(t, peaks(2, 5), s.document(collection, "node:mn-1", sampleHour))
			require.Equal(t, peaks(1, 2), s.document(collection, "node:mn-2", sampleHour))
		}
	})

	t.Run("idle live node", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"mn-1": 1}, nil)
		s.live = []string{"mn-1", "mn-3"}
		a, _, _ := newTestAggregator(s, sampleHour)
		require.NoError(t, a.run(ctx))
		require.Equal(t, peaks(0, 0), s.document(schema.CollectionAnalyticsDaily, "node:mn-3", sampleDay))
		require.Equal(t, peaks(1, 0), s.document(schema.CollectionAnalyticsDaily, "node:mn-1", sampleDay))
	})

	t.Run("server without a label", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"": 2}, map[string]int32{"": 3})
		a, _, _ := newTestAggregator(s, sampleHour)
		require.NoError(t, a.run(ctx))
		for _, increment := range s.increments {
			require.Equal(t, "global", increment.Scope)
		}
	})

	t.Run("run repeated", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"": 3}, map[string]int32{"": 1})
		a, _, _ := newTestAggregator(s, sampleHour)
		require.NoError(t, a.run(ctx))
		first := s.document(schema.CollectionAnalytics5Min, "global", sampleHour)
		require.NoError(t, a.run(ctx))
		require.Equal(t, first, s.document(schema.CollectionAnalytics5Min, "global", sampleHour))
		require.Equal(t, peaks(3, 1), first)
	})
}

func TestAggregatorUniqueIdentities(t *testing.T) {
	ctx := context.Background()
	yesterday := sampleDay.Add(-24 * time.Hour)

	t.Run("day closed", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.pendingDays = []time.Time{yesterday}
		s.unique[yesterday] = 3
		a, logs, _ := newTestAggregator(s, sampleDay.Add(6*time.Minute))
		require.NoError(t, a.run(ctx))
		require.Equal(t, map[time.Time]int32{yesterday: 3}, s.set)
		require.NotEmpty(t, logs.matching("debug", "unique identities"))
	})

	t.Run("day just closed", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.pendingDays = []time.Time{yesterday}
		a, _, _ := newTestAggregator(s, sampleDay.Add(3*time.Minute))
		require.NoError(t, a.run(ctx))
		require.Empty(t, s.set)
	})

	t.Run("days beyond the entities retention", func(t *testing.T) {
		s := newFakeAggregatorStore()
		old := sampleDay.Add(-90 * 24 * time.Hour)
		s.pendingDays = []time.Time{old, yesterday}
		a, _, _ := newTestAggregator(s, sampleDay.Add(time.Hour))
		require.NoError(t, a.run(ctx))
		require.Equal(t, map[time.Time]int32{yesterday: 0}, s.set, "an idle day gets 0")
	})

	t.Run("at most seven days per run", func(t *testing.T) {
		s := newFakeAggregatorStore()
		for i := range 9 {
			s.pendingDays = append(s.pendingDays, sampleDay.Add(-time.Duration(9-i)*24*time.Hour))
		}
		a, _, _ := newTestAggregator(s, sampleDay.Add(time.Hour))
		require.NoError(t, a.run(ctx))
		require.Len(t, s.set, uniqueIdentityDaysPerRun)
		require.NoError(t, a.run(ctx))
		require.Len(t, s.set, 9)
	})
}

func TestAggregatorFailures(t *testing.T) {
	s := newFakeAggregatorStore()
	s.countErr = errors.New("connection refused")
	s.pendingDays = []time.Time{sampleDay.Add(-24 * time.Hour)}
	a, logs, m := newTestAggregator(s, sampleDay.Add(time.Hour))
	require.Error(t, a.run(context.Background()))
	require.Zero(t, s.applied(), "nothing is written")
	require.Empty(t, s.set, "the unique identities wait for the next run")
	require.Len(t, logs.matching("warn", "analytics aggregator: run failed"), 1)
	require.Equal(t, float64(1), testutil.ToFloat64(m.runs.WithLabelValues(resultError)))
	require.Zero(t, testutil.ToFloat64(m.runs.WithLabelValues(resultOK)))
}

func TestAggregatorMetrics(t *testing.T) {
	s := newFakeAggregatorStore()
	a, _, m := newTestAggregator(s, sampleHour)
	require.NoError(t, a.run(context.Background()))
	require.Equal(t, float64(1), testutil.ToFloat64(m.runs.WithLabelValues(resultOK)))
	require.Equal(t, 1, testutil.CollectAndCount(m.runDuration))
	problems, err := testutil.CollectAndLint(m.runs)
	require.NoError(t, err)
	require.Empty(t, problems)
}

func TestAggregatorLoop(t *testing.T) {
	t.Run("one node runs at a time, right after starting and every interval", func(t *testing.T) {
		locks := &fakeLocker{}
		var running, overlaps atomic.Int32
		stores := []*fakeAggregatorStore{newFakeAggregatorStore(), newFakeAggregatorStore()}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for _, s := range stores {
			s.running, s.overlaps = &running, &overlaps
			a, _, _ := newTestAggregator(s, sampleHour)
			a.settings.interval = 50 * time.Millisecond
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.loop(ctx, locks)
			}()
		}
		runs := func() int { return (stores[0].applied() + stores[1].applied()) / 3 }
		require.Eventually(t, func() bool { return runs() >= 6 }, 5*time.Second, 10*time.Millisecond)
		cancel()
		wg.Wait()
		require.Zero(t, overlaps.Load(), "two runs overlapped")
	})

	t.Run("the first run does not wait for the interval", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.counts(map[string]int32{"": 2}, nil)
		a, _, _ := newTestAggregator(s, sampleHour)
		a.settings.interval = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.loop(ctx, &localLocker{})
		}()
		require.Eventually(t, func() bool {
			return s.document(schema.CollectionAnalytics5Min, "global", sampleHour) != nil
		}, time.Second, 10*time.Millisecond)
		cancel()
		<-done
		require.Equal(t, int32(2), s.document(schema.CollectionAnalytics5Min, "global", sampleHour)["rooms.peakActive"])
	})

	t.Run("a failed run leaves the next one to the schedule", func(t *testing.T) {
		s := newFakeAggregatorStore()
		s.countErr = errors.New("connection refused")
		a, _, m := newTestAggregator(s, sampleHour)
		a.settings.interval = 200 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.loop(ctx, &localLocker{})
		}()
		failed := func() float64 { return testutil.ToFloat64(m.runs.WithLabelValues(resultError)) }
		require.Eventually(t, func() bool { return failed() == 1 }, time.Second, 5*time.Millisecond)
		time.Sleep(100 * time.Millisecond)
		require.Equal(t, float64(1), failed(), "no retry before the interval")
		require.Eventually(t, func() bool { return failed() == 2 }, time.Second, 5*time.Millisecond)
		cancel()
		<-done
	})

	t.Run("the lock is obtained again after a Redis restart", func(t *testing.T) {
		mr := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		s := newFakeAggregatorStore()
		a, logs, _ := newTestAggregator(s, sampleHour)
		a.settings.interval = 200 * time.Millisecond
		a.lockErrorPause = 100 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.loop(ctx, redisLocker{client: redislock.New(client), backoff: 50 * time.Millisecond})
		}()
		require.Eventually(t, func() bool { return s.applied() >= 6 }, 5*time.Second, 10*time.Millisecond)

		mr.Close()
		time.Sleep(300 * time.Millisecond)
		require.NoError(t, mr.Restart())
		mr.FlushAll()
		restarted := s.applied()
		require.Eventually(t, func() bool { return s.applied() >= restarted+6 },
			a.lockErrorPause+4*a.settings.interval+time.Second, 20*time.Millisecond)
		cancel()
		<-done
		require.LessOrEqual(t, len(logs.matching("warn", "cannot obtain the lock")), 1, "a Redis outage is logged once")
	})
}

func TestAggregatorRunsWhateverTheReconciler(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.Reconciler.Enabled = &enabled })
		runner, err := newAggregatorRunner(&config.Config{}, settings, nil)
		require.NoError(t, err)
		require.NotNil(t, runner)
		require.IsType(t, &localLocker{}, runner.locks, "without Redis the node elects itself")
		require.Equal(t, time.Minute, runner.aggregator.settings.interval)
		require.Equal(t, 65*time.Second, runner.aggregator.settings.lockTTL())
	}
}

func TestJobLocksAreTheOnlyAnalyticsKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	locks := redisLocker{client: redislock.New(client), backoff: 50 * time.Millisecond}

	r, _ := newTestReconciler(newFakeReconcilerStore(), newFakeLive())
	r.settings.interval = time.Hour
	a, _, _ := newTestAggregator(newFakeAggregatorStore(), sampleHour)
	a.settings.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.loop(ctx, locks)
	}()
	go func() {
		defer wg.Done()
		a.loop(ctx, locks)
	}()
	want := []string{aggregatorLockKey, reconcilerLockKey}
	require.Eventually(t, func() bool { return slices.Equal(want, mr.Keys()) }, 5*time.Second, 10*time.Millisecond)
	cancel()
	wg.Wait()
	require.Equal(t, "analytics-aggregator-lock", aggregatorLockKey)
}
