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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
)

// panickingStore panics in its first ApplyUpdates calls, as 3.9.0's writer did on NaN stats.
type panickingStore struct {
	*fakeStore
	panics atomic.Int32
}

func (p *panickingStore) ApplyUpdates(ctx context.Context, updates []projection.Update) error {
	if p.panics.Add(-1) >= 0 {
		panic("assignment to entry in nil map")
	}
	return p.fakeStore.ApplyUpdates(ctx, updates)
}

func TestGuardedReturnsThePanicAsAnError(t *testing.T) {
	err := guarded("a test", func() error { panic("boom") })
	require.ErrorIs(t, err, errPanic)
	require.ErrorContains(t, err, "boom")
	require.False(t, isTransient(err), "the work that panicked is dropped, not retried")

	plain := errors.New("plain")
	require.Equal(t, plain, guarded("a test", func() error { return plain }))
	require.NoError(t, guarded("a test", func() error { return nil }))
}

func TestBatchThatPanicsIsDropped(t *testing.T) {
	store := &panickingStore{fakeStore: &fakeStore{}}
	store.panics.Store(1)
	w := newWriter(store, testSettings(t, nil), Node{ID: testNode}, newMetrics())
	before := counterValue(w.metrics.dropped, "events", "error")

	w.enqueueEvents(roomEvents(2))
	require.ErrorIs(t, w.tick(context.Background()), errPanic)
	require.Zero(t, w.events.Len())
	require.Equal(t, float64(2), counterValue(w.metrics.dropped, "events", "error")-before)
	require.Equal(t, 1, store.heartbeats, "the node keeps its heartbeat")

	w.enqueueEvents(roomEvents(1))
	require.NoError(t, w.tick(context.Background()))
	require.Len(t, store.writtenEvents(), 1)
}

func TestLeaderJobSurvivesACycleThatPanics(t *testing.T) {
	locks := &fakeLocker{}
	var cycles atomic.Int32
	job := &leaderJob{name: "test job", key: "test-lock", interval: time.Millisecond, ttl: time.Second,
		lockErrorPause: time.Millisecond, log: defaultJobLog(),
		cycle: func(context.Context, lease, time.Duration) {
			if cycles.Add(1) == 1 {
				panic("boom")
			}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.loop(ctx, locks)
	}()
	require.Eventually(t, func() bool { return cycles.Load() >= 3 }, 5*time.Second, time.Millisecond)
	cancel()
	<-done
	locks.mu.Lock()
	defer locks.mu.Unlock()
	require.Zero(t, locks.holder, "the lock is released after every cycle")
}

func TestMigrationPhaseThatPanicsIsNotRetried(t *testing.T) {
	m := &migrator{backoff: time.Millisecond, log: defaultJobLog()}
	calls := 0
	err := m.withRetries(context.Background(), func() error {
		calls++
		panic("boom")
	})
	require.ErrorIs(t, err, errPanic)
	require.Equal(t, 1, calls)
}

func TestTaskThatPanicsStops(t *testing.T) {
	tk := &task{loop: func(context.Context) { panic("boom") }}
	tk.start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tk.stop(ctx)
	require.NoError(t, ctx.Err(), "the task ended and stop did not wait for its deadline")
}
