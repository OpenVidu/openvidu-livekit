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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/livekit/protocol/livekit"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/pkg/config"
)

var reconcileNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// fakeLive is LiveKit's live state: rooms by name with their participants, and node availability.
type fakeLive struct {
	mu           sync.Mutex
	rooms        map[string]*livekit.Room
	participants map[string][]string
	available    map[string]bool
	roomsErr     error
	nodesErr     error
	registryHits int
}

func newFakeLive() *fakeLive {
	return &fakeLive{rooms: map[string]*livekit.Room{}, participants: map[string][]string{}, available: map[string]bool{}}
}

func (f *fakeLive) addRoom(name string, sid string, participants ...string) {
	f.rooms[name] = &livekit.Room{Name: name, Sid: sid}
	f.participants[name] = participants
}

func (f *fakeLive) ListActiveRooms() ([]*livekit.Room, error) {
	if f.roomsErr != nil {
		return nil, f.roomsErr
	}
	var rooms []*livekit.Room
	for _, room := range f.rooms {
		rooms = append(rooms, room)
	}
	return rooms, nil
}

func (f *fakeLive) ListParticipants(roomName string) ([]*livekit.ParticipantInfo, error) {
	var infos []*livekit.ParticipantInfo
	for _, sid := range f.participants[roomName] {
		infos = append(infos, &livekit.ParticipantInfo{Sid: sid})
	}
	return infos, nil
}

func (f *fakeLive) IsNodeAvailable(nodeID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registryHits++
	if f.nodesErr != nil {
		return false, f.nodesErr
	}
	return f.available[nodeID], nil
}

// fakeReconcilerStore answers the reads of the reconciler from memory; methods a test does not reach are left to the
// embedded nil interface.
type fakeReconcilerStore struct {
	reconcilerStore
	mu           sync.Mutex
	lastSeen     map[string]time.Time
	liveNodes    []string
	stale        []store.OpenParticipant
	closed       map[string]bool
	runs         []schema.SchemaMetaReconciler
	reconcileHit atomic.Int32
}

func newFakeReconcilerStore() *fakeReconcilerStore {
	return &fakeReconcilerStore{lastSeen: map[string]time.Time{}, closed: map[string]bool{}}
}

func (f *fakeReconcilerStore) NodesLastSeen(_ context.Context, ids []string) (map[string]time.Time, error) {
	found := map[string]time.Time{}
	for _, id := range ids {
		if at, ok := f.lastSeen[id]; ok {
			found[id] = at
		}
	}
	return found, nil
}

func (f *fakeReconcilerStore) LiveRoomNodes(context.Context, time.Time, int) ([]string, error) {
	return f.liveNodes, nil
}

func (f *fakeReconcilerStore) Rooms(context.Context, []string) (map[string]store.RoomRef, error) {
	return map[string]store.RoomRef{}, nil
}

func (f *fakeReconcilerStore) StaleParticipants(_ context.Context, _ time.Time, limit int) ([]store.OpenParticipant, error) {
	f.reconcileHit.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	var open []store.OpenParticipant
	for _, participant := range f.stale {
		if !f.closed[participant.SID] && len(open) < limit {
			open = append(open, participant)
		}
	}
	return open, nil
}

func (f *fakeReconcilerStore) StaleRooms(context.Context, time.Time, time.Duration, time.Time, int) ([]store.OpenRoom, error) {
	return nil, nil
}

func (f *fakeReconcilerStore) OpenEgresses(context.Context, int) ([]store.OpenEgress, error) {
	return nil, nil
}

func (f *fakeReconcilerStore) OpenIngressSessions(context.Context, int) ([]store.OpenIngressSession, error) {
	return nil, nil
}

func (f *fakeReconcilerStore) Tracks(context.Context, []string, []string) (map[string]projection.TrackRef, map[string][]string, error) {
	return nil, map[string][]string{}, nil
}

func (f *fakeReconcilerStore) CloseEntity(_ context.Context, update projection.Update) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed[update.ID] {
		return false, nil
	}
	f.closed[update.ID] = true
	return true, nil
}

func (f *fakeReconcilerStore) ApplyCascadesCounted(context.Context, []projection.Update) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (f *fakeReconcilerStore) ApplyUpdates(context.Context, []projection.Update) error { return nil }

func (f *fakeReconcilerStore) InsertEvents(context.Context, []schema.EventDocument) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (f *fakeReconcilerStore) ApplyIncrements(context.Context, []projection.Increment) error {
	return nil
}

func (f *fakeReconcilerStore) DaysWithoutUniqueIdentities(context.Context, time.Time, time.Time, int) ([]time.Time, error) {
	return nil, nil
}

func (f *fakeReconcilerStore) RecordReconcilerRun(_ context.Context, run schema.SchemaMetaReconciler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

func (f *fakeReconcilerStore) recorded() []schema.SchemaMetaReconciler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]schema.SchemaMetaReconciler(nil), f.runs...)
}

type capturedLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLog) add(level string, msg string, keysAndValues ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprint(append([]any{level, " ", msg}, keysAndValues...)...))
}

func (c *capturedLog) sink() reconcilerLog {
	return reconcilerLog{
		debugw: func(msg string, kv ...any) { c.add("debug", msg, kv...) },
		infow:  func(msg string, kv ...any) { c.add("info", msg, kv...) },
		warnw:  func(msg string, err error, kv ...any) { c.add("warn", msg, append(kv, "error", err)...) },
	}
}

func (c *capturedLog) matching(level string, part string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []string
	for _, line := range c.lines {
		if strings.HasPrefix(line, level) && strings.Contains(line, part) {
			found = append(found, line)
		}
	}
	return found
}

func testReconcilerSettings() reconcilerSettings {
	return reconcilerSettings{batchInterval: 10 * time.Second, interval: time.Minute, cleanupInterval: 3 * time.Minute,
		maxClosures: 500, entitiesRetention: 2160 * time.Hour}
}

func newTestReconciler(s reconcilerStore, live liveState) (*reconciler, *capturedLog) {
	r := newReconciler(s, live, projection.Options{}, testReconcilerSettings(), "ND_leader", buildReconcilerMetrics())
	logs := &capturedLog{}
	r.log = logs.sink()
	r.now = func() time.Time { return reconcileNow }
	return r, logs
}

func ago(d time.Duration) *time.Time {
	t := reconcileNow.Add(-d)
	return &t
}

func openParticipant(sid string, room string, node string) store.OpenParticipant {
	return store.OpenParticipant{SID: sid, RoomSID: "RM_" + room, RoomName: room, NodeID: node, Status: "active",
		JoinedAt: reconcileNow.Add(-time.Hour), LastStatsAt: ago(100 * time.Second), LastEventAt: ago(30 * time.Minute)}
}

func confirmedSids(c confirmation) map[string]string {
	sids := map[string]string{}
	for _, participant := range c.participants {
		sids[participant.SID] = participant.reason
	}
	for _, room := range c.rooms {
		sids[room.SID] = room.reason
	}
	return sids
}

func TestDeadNodes(t *testing.T) {
	s := newFakeReconcilerStore()
	s.lastSeen["ND_fresh"] = reconcileNow.Add(-5 * time.Second)
	s.lastSeen["ND_stale"] = reconcileNow.Add(-45 * time.Second)
	s.lastSeen["ND_registered"] = reconcileNow.Add(-45 * time.Second)
	live := newFakeLive()
	live.available["ND_registered"] = true
	r, _ := newTestReconciler(s, live)

	dead, seen, err := r.deadNodes(context.Background(), reconcileNow,
		[]string{"ND_fresh", "ND_stale", "ND_registered", "ND_unknown", "", "ND_stale"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"ND_stale": true, "ND_unknown": true}, dead)
	require.Equal(t, map[string]bool{"ND_fresh": true, "ND_stale": true, "ND_registered": true}, seen)
	require.Equal(t, 3, live.registryHits, "a fresh heartbeat needs no registry read")

	live.nodesErr = errors.New("redis down")
	dead, _, err = r.deadNodes(context.Background(), reconcileNow, []string{"ND_stale"})
	require.NoError(t, err)
	require.Empty(t, dead, "an unreadable registry never makes a node dead")
}

func TestLegacyCandidatesHeldDuringTheMigration(t *testing.T) {
	s := newFakeReconcilerStore()
	s.lastSeen["ND_crashed"] = reconcileNow.Add(-time.Hour)
	s.stale = []store.OpenParticipant{openParticipant("PA_legacy", "old", "ND_legacy"),
		openParticipant("PA_crashed", "daily", "ND_crashed")}
	r, _ := newTestReconciler(s, newFakeLive())
	hold := true
	r.holdLegacy = func(context.Context) (bool, error) { return hold, nil }

	found, err := r.candidates(context.Background(), reconcileNow)
	require.NoError(t, err)
	require.Len(t, found.participants, 1)
	require.Equal(t, "PA_crashed", found.participants[0].SID, "a node that wrote a heartbeat is not legacy")

	hold = false
	found, err = r.candidates(context.Background(), reconcileNow)
	require.NoError(t, err)
	require.Len(t, found.participants, 2)
}

func TestConfirmation(t *testing.T) {
	ctx := context.Background()
	alive := openParticipant("PA_alive", "daily", "ND_alive")
	present := openParticipant("PA_present", "daily", "ND_alive")
	crashed := openParticipant("PA_crashed", "daily", "ND_dead")
	candidates := func() candidateSet {
		return candidateSet{participants: []store.OpenParticipant{alive, present, crashed},
			dead: map[string]bool{"ND_dead": true}}
	}
	liveWithDaily := func() *fakeLive {
		live := newFakeLive()
		live.addRoom("daily", "RM_daily", "PA_present", "PA_crashed")
		return live
	}

	t.Run("dead node confirms without the listing, alive nodes need two absences", func(t *testing.T) {
		r, logs := newTestReconciler(newFakeReconcilerStore(), liveWithDaily())
		r.run = 1
		confirmed, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"PA_crashed": reasonNodeDead}, confirmedSids(confirmed),
			"absent once is not enough, present is kept")
		require.Len(t, logs.matching("warn", "PA_present (lastStatsAt"), 1)

		r.run = 2
		confirmed, err = r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"PA_crashed": reasonNodeDead, "PA_alive": reasonAbsentLive},
			confirmedSids(confirmed))
		require.Equal(t, crashed.LastStatsAt.UTC(), confirmed.participants[0].at, "end time is the last stats flush")
	})

	t.Run("a follow-up run is not a consecutive run", func(t *testing.T) {
		r, _ := newTestReconciler(newFakeReconcilerStore(), liveWithDaily())
		r.run = 1
		_, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		confirmed, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.NotContains(t, confirmedSids(confirmed), "PA_alive")
	})

	t.Run("a run in between resets the count", func(t *testing.T) {
		r, _ := newTestReconciler(newFakeReconcilerStore(), liveWithDaily())
		r.run = 1
		_, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		r.run = 3
		confirmed, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.NotContains(t, confirmedSids(confirmed), "PA_alive")
	})

	t.Run("a listing error confirms nothing of alive nodes and restarts the count", func(t *testing.T) {
		live := liveWithDaily()
		r, _ := newTestReconciler(newFakeReconcilerStore(), live)
		r.run = 1
		_, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)

		live.roomsErr = errors.New("redis: connection refused")
		r.run = 2
		report := &runReport{result: resultOK}
		confirmed, err := r.confirm(ctx, reconcileNow, candidates(), report)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"PA_crashed": reasonNodeDead}, confirmedSids(confirmed))
		require.Equal(t, resultLiveStateError, report.result)

		live.roomsErr = nil
		r.run = 3
		confirmed, err = r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.NotContains(t, confirmedSids(confirmed), "PA_alive", "the absence count started again from zero")
	})

	t.Run("an empty listing while rooms of alive nodes receive stats is untrusted", func(t *testing.T) {
		s := newFakeReconcilerStore()
		s.liveNodes = []string{"ND_alive"}
		s.lastSeen["ND_alive"] = reconcileNow.Add(-2 * time.Second)
		r, _ := newTestReconciler(s, newFakeLive())
		for run := 1; run <= 3; run++ {
			r.run = run
			report := &runReport{result: resultOK}
			confirmed, err := r.confirm(ctx, reconcileNow, candidates(), report)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"PA_crashed": reasonNodeDead}, confirmedSids(confirmed))
			require.Equal(t, resultLiveStateUntrusted, report.result)
		}
	})

	t.Run("an empty listing without live rooms is trusted", func(t *testing.T) {
		r, _ := newTestReconciler(newFakeReconcilerStore(), newFakeLive())
		r.run = 1
		_, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		r.run = 2
		confirmed, err := r.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.Contains(t, confirmedSids(confirmed), "PA_present", "with no room listed, every participant is absent")
	})

	t.Run("a new leader starts counting from zero", func(t *testing.T) {
		live := liveWithDaily()
		first, _ := newTestReconciler(newFakeReconcilerStore(), live)
		first.run = 1
		_, err := first.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)

		next, _ := newTestReconciler(newFakeReconcilerStore(), live)
		next.run = 1
		confirmed, err := next.confirm(ctx, reconcileNow, candidates(), &runReport{result: resultOK})
		require.NoError(t, err)
		require.NotContains(t, confirmedSids(confirmed), "PA_alive")
	})

	t.Run("rooms are absent when LiveKit lists another room with their name", func(t *testing.T) {
		live := newFakeLive()
		live.addRoom("daily", "RM_recreated")
		r, _ := newTestReconciler(newFakeReconcilerStore(), live)
		room := store.OpenRoom{SID: "RM_daily", Name: "daily", NodeID: "ND_alive", LastEventAt: reconcileNow.Add(-time.Hour)}
		found := candidateSet{rooms: []store.OpenRoom{room}, dead: map[string]bool{}}
		for run := 1; run <= 2; run++ {
			r.run = run
			confirmed, err := r.confirm(ctx, reconcileNow, found, &runReport{result: resultOK})
			require.NoError(t, err)
			if run == 2 {
				require.Equal(t, map[string]string{"RM_daily": reasonAbsentLive}, confirmedSids(confirmed))
			}
		}
	})
}

// fakeLocker grants the lock to one holder at a time, like Redis.
type fakeLocker struct {
	mu      sync.Mutex
	holder  int
	nextID  int
	obtains atomic.Int32
}

type fakeLease struct {
	locker *fakeLocker
	id     int
}

func (l *fakeLocker) Obtain(ctx context.Context, _ string, _ time.Duration) (lease, error) {
	l.obtains.Add(1)
	l.mu.Lock()
	if l.holder == 0 {
		l.nextID++
		l.holder = l.nextID
		l.mu.Unlock()
		return fakeLease{l, l.holder}, nil
	}
	l.mu.Unlock()
	sleep(ctx, 10*time.Millisecond)
	return nil, redislock.ErrNotObtained
}

func (l fakeLease) Refresh(context.Context, time.Duration) error { return nil }

func (l fakeLease) Release(context.Context) error {
	l.locker.mu.Lock()
	defer l.locker.mu.Unlock()
	if l.locker.holder == l.id {
		l.locker.holder = 0
	}
	return nil
}

func TestLoop(t *testing.T) {
	t.Run("one node runs at a time, right after starting and every interval", func(t *testing.T) {
		locks := &fakeLocker{}
		stores := []*fakeReconcilerStore{newFakeReconcilerStore(), newFakeReconcilerStore()}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		var running, overlaps atomic.Int32
		for _, s := range stores {
			r, _ := newTestReconciler(&overlapStore{fakeReconcilerStore: s, running: &running, overlaps: &overlaps}, newFakeLive())
			r.settings.interval = 50 * time.Millisecond
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.loop(ctx, locks)
			}()
		}
		require.Eventually(t, func() bool { return len(stores[0].recorded())+len(stores[1].recorded()) >= 6 },
			5*time.Second, 10*time.Millisecond)
		cancel()
		wg.Wait()
		require.Zero(t, overlaps.Load(), "two runs overlapped")
	})

	t.Run("a partial run continues while holding the lock", func(t *testing.T) {
		s := newFakeReconcilerStore()
		for i := range 12 {
			participant := openParticipant(fmt.Sprintf("PA_%02d", i), "daily", "ND_dead")
			s.stale = append(s.stale, participant)
		}
		r, _ := newTestReconciler(s, newFakeLive())
		r.settings.maxClosures = 5
		r.settings.interval = time.Hour
		r.continuationDelay = 20 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.loop(ctx, &localLocker{})
		}()
		require.Eventually(t, func() bool { return len(s.recorded()) >= 3 }, 5*time.Second, 10*time.Millisecond)
		time.Sleep(100 * time.Millisecond)
		cancel()
		<-done
		runs := s.recorded()
		require.Equal(t, []bool{true, true, false}, []bool{runs[0].Partial, runs[1].Partial, runs[2].Partial})
		require.Equal(t, []int32{5, 5, 2}, []int32{runs[0].Closed.Participant, runs[1].Closed.Participant,
			runs[2].Closed.Participant})
		require.Len(t, runs, 3, "after the last partial run the loop waits a full interval")
	})

	t.Run("the lock is obtained again after a Redis restart", func(t *testing.T) {
		mr := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		s := newFakeReconcilerStore()
		r, logs := newTestReconciler(s, newFakeLive())
		r.settings.interval = 200 * time.Millisecond
		r.lockErrorPause = 100 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.loop(ctx, redisLocker{client: redislock.New(client), backoff: 50 * time.Millisecond})
		}()
		require.Eventually(t, func() bool { return len(s.recorded()) >= 2 }, 5*time.Second, 10*time.Millisecond)

		mr.Close()
		time.Sleep(300 * time.Millisecond)
		require.NoError(t, mr.Restart())
		mr.FlushAll()
		restarted := len(s.recorded())
		require.Eventually(t, func() bool { return len(s.recorded()) >= restarted+2 },
			r.lockErrorPause+4*r.settings.interval+time.Second, 20*time.Millisecond)
		cancel()
		<-done
		require.LessOrEqual(t, len(logs.matching("warn", "cannot obtain the lock")), 1, "a Redis outage is logged once")
	})
}

// overlapStore counts runs that start while another run is in progress.
type overlapStore struct {
	*fakeReconcilerStore
	running  *atomic.Int32
	overlaps *atomic.Int32
}

func (s *overlapStore) StaleParticipants(ctx context.Context, before time.Time, limit int) ([]store.OpenParticipant, error) {
	if s.running.Add(1) > 1 {
		s.overlaps.Add(1)
	}
	time.Sleep(5 * time.Millisecond)
	return s.fakeReconcilerStore.StaleParticipants(ctx, before, limit)
}

func (s *overlapStore) RecordReconcilerRun(ctx context.Context, run schema.SchemaMetaReconciler) error {
	s.running.Add(-1)
	return s.fakeReconcilerStore.RecordReconcilerRun(ctx, run)
}

func TestReconcilerDisabled(t *testing.T) {
	settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.Reconciler.Enabled = new(bool) })
	runner, err := newReconcilerRunner(&config.Config{}, settings, nil, newFakeLive(), Node{ID: "ND_a"})
	require.NoError(t, err)
	require.Nil(t, runner)

	settings = testSettings(t, nil)
	runner, err = newReconcilerRunner(&config.Config{}, settings, nil, newFakeLive(), Node{ID: "ND_a"})
	require.NoError(t, err)
	require.NotNil(t, runner)
	require.IsType(t, &localLocker{}, runner.locks, "without Redis the node elects itself")
}
