package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/livekit-server/pkg/telemetry/telemetryfakes"
)

// launcherFake answers StopEgress with the configured error per egress id
// (a missing id answers like a live handler) and records who was asked.
type launcherFake struct {
	answers      map[string]error
	asked        []string
	beforeAnswer func(egressId string) // what happens elsewhere while the probe is in flight
	entered      chan string           // a gated probe reports here that it started...
	release      chan struct{}         // ...and answers once this is closed
}

func (f *launcherFake) StartEgress(context.Context, *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
	return nil, errors.New("not used")
}

func (f *launcherFake) StopEgress(ctx context.Context, req *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	f.asked = append(f.asked, req.EgressId)
	if f.beforeAnswer != nil {
		f.beforeAnswer(req.EgressId)
	}
	switch f.answers[req.EgressId] {
	case errHangs:
		// like psrpc when the caller's deadline expires before its own timeout
		<-ctx.Done()
		return nil, psrpc.ErrRequestCanceled
	case errGated:
		f.entered <- req.EgressId
		<-f.release
		return nil, psrpc.ErrRequestTimedOut
	}
	return &livekit.EgressInfo{EgressId: req.EgressId}, f.answers[req.EgressId]
}

var (
	errHangs = errors.New("hangs until the probe gives up")
	errGated = errors.New("answers when the test says so")
)

// ioStub is the narrowed IO client the egress service holds, with the
// UpdateEgress the lost-egress fallback looks for underneath, over the store.
type ioStub struct {
	IOClient  // only GetEgress and UpdateEgress are reached
	store     *RedisStore
	updateErr error
	updates   int
}

func (s *ioStub) GetEgress(ctx context.Context, req *rpc.GetEgressRequest) (*livekit.EgressInfo, error) {
	return s.store.LoadEgress(ctx, req.EgressId)
}

func (s *ioStub) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) (*emptypb.Empty, error) {
	s.updates++
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	return &emptypb.Empty{}, s.store.UpdateEgress(ctx, info)
}

// plainIOStub is an IO client with no UpdateEgress underneath, which the
// production one never is (see the egressUpdater assertion).
type plainIOStub struct {
	IOClient
	store *RedisStore
}

func (s *plainIOStub) GetEgress(ctx context.Context, req *rpc.GetEgressRequest) (*livekit.EgressInfo, error) {
	return s.store.LoadEgress(ctx, req.EgressId)
}

var errRedisHiccup = errors.New("redis: connection reset by peer")

// failingStore is the shared store during a Redis hiccup: the named call
// fails, every other one works.
type failingStore struct {
	reconcileStore
	failing string
}

func (s *failingStore) ListEgress(ctx context.Context, roomName livekit.RoomName, active bool) ([]*livekit.EgressInfo, error) {
	if s.failing == "ListEgress" {
		return nil, errRedisHiccup
	}
	return s.reconcileStore.ListEgress(ctx, roomName, active)
}

func (s *failingStore) ListIngress(ctx context.Context, roomName livekit.RoomName) ([]*livekit.IngressInfo, error) {
	if s.failing == "ListIngress" {
		return nil, errRedisHiccup
	}
	return s.reconcileStore.ListIngress(ctx, roomName)
}

func (s *failingStore) LoadRoom(ctx context.Context, roomName livekit.RoomName, includeInternal bool) (*livekit.Room, *livekit.RoomInternal, error) {
	if s.failing == "LoadRoom" {
		return nil, nil, errRedisHiccup
	}
	return s.reconcileStore.LoadRoom(ctx, roomName, includeInternal)
}

func (s *failingStore) HasParticipant(ctx context.Context, roomName livekit.RoomName, identity livekit.ParticipantIdentity) (bool, error) {
	if s.failing == "HasParticipant" {
		return false, errRedisHiccup
	}
	return s.reconcileStore.HasParticipant(ctx, roomName, identity)
}

type reconcileFixture struct {
	ctx      context.Context
	store    *RedisStore
	launcher *launcherFake
	tel      *telemetryfakes.FakeTelemetryService
	now      time.Time
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	_, rc := newMiniredis(t)
	f := &reconcileFixture{
		ctx:      context.Background(),
		store:    NewRedisStore(rc),
		launcher: &launcherFake{answers: map[string]error{}},
		tel:      &telemetryfakes.FakeTelemetryService{},
		now:      time.Now(),
	}
	previous := reconcileNow
	reconcileNow = func() time.Time { return f.now }
	t.Cleanup(func() { reconcileNow = previous })
	return f
}

func (f *reconcileFixture) run() { reconcileEntities(f.ctx, f.store, f.launcher, f.tel) }

// lockIsFree proves the pass released the lock, so the next tick can take it
func (f *reconcileFixture) lockIsFree(t *testing.T) {
	lock, err := redislock.New(f.store.rc).Obtain(context.Background(), reconcileLockKey, time.Second, nil)
	require.NoError(t, err, "the lock should have been released")
	require.NoError(t, lock.Release(context.Background()))
}

func (f *reconcileFixture) room(t *testing.T, name string, participants ...string) {
	require.NoError(t, f.store.StoreRoom(f.ctx, &livekit.Room{Sid: "RM_" + name, Name: name}, nil))
	for _, identity := range participants {
		require.NoError(t, f.store.StoreParticipant(f.ctx, livekit.RoomName(name),
			&livekit.ParticipantInfo{Sid: "PA_" + identity, Identity: identity}))
	}
}

func (f *reconcileFixture) egress(t *testing.T, id, room string, status livekit.EgressStatus, updatedAt time.Time) {
	require.NoError(t, f.store.StoreEgress(f.ctx, &livekit.EgressInfo{
		EgressId: id, RoomName: room, Status: status, UpdatedAt: updatedAt.UnixNano(),
	}))
}

// egressInRoom is an egress row that also records the sid of the room it was started in
func (f *reconcileFixture) egressInRoom(t *testing.T, id, room, roomSid string) {
	require.NoError(t, f.store.StoreEgress(f.ctx, &livekit.EgressInfo{
		EgressId: id, RoomName: room, RoomId: roomSid, Status: livekit.EgressStatus_EGRESS_ACTIVE, UpdatedAt: f.now.Add(-time.Hour).UnixNano(),
	}))
	f.launcher.answers[id] = psrpc.ErrRequestTimedOut
}

func (f *reconcileFixture) egressStatus(t *testing.T, id string) livekit.EgressStatus {
	info, err := f.store.LoadEgress(f.ctx, id)
	require.NoError(t, err)
	return info.Status
}

func (f *reconcileFixture) ingress(t *testing.T, id, room, identity string, status livekit.IngressState_Status, since time.Time) {
	require.NoError(t, f.store.StoreIngress(f.ctx, &livekit.IngressInfo{
		IngressId: id, Name: id, StreamKey: "key-" + id, InputType: livekit.IngressInput_RTMP_INPUT,
		RoomName: room, ParticipantIdentity: identity,
	}))
	require.NoError(t, f.store.UpdateIngressState(f.ctx, id, &livekit.IngressState{
		Status: status, RoomId: "RM_" + room, ResourceId: "res-" + id,
		StartedAt: since.UnixNano(), UpdatedAt: since.UnixNano(),
		Tracks: []*livekit.TrackInfo{{Sid: "TR_" + id}},
	}))
}

func (f *reconcileFixture) ingressState(t *testing.T, id string) *livekit.IngressState {
	info, err := f.store.LoadIngress(f.ctx, id)
	require.NoError(t, err)
	return info.State
}

func TestReconcileLeavesEgressesOfLiveRoomsAlone(t *testing.T) {
	f := newReconcileFixture(t)
	f.room(t, "live")
	f.egress(t, "EG_live", "live", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_live"] = psrpc.ErrRequestTimedOut // would count as dead, if asked

	f.run()

	require.Empty(t, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_live"))
}

func TestReconcileMarksLostTheEgressOfAGoneRoomNobodyAnswersFor(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut

	f.run()

	require.Equal(t, []string{"EG_lost"}, f.launcher.asked)
	info, err := f.store.LoadEgress(f.ctx, "EG_lost")
	require.NoError(t, err)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	require.Equal(t, egressLostError, info.Error)
	require.Equal(t, f.now.UnixNano(), info.EndedAt)
	require.Equal(t, 1, f.tel.EgressEndedCallCount())
	// the sweeper now knows about it
	ended, err := f.store.rc.HGet(f.ctx, EndedEgressKey, "EG_lost").Result()
	require.NoError(t, err)
	require.Equal(t, egressEndedValue("gone", f.now.UnixNano()), ended)
	// terminal rows are not active any more, so a second run has nothing to do
	f.launcher.asked = nil
	f.run()
	require.Empty(t, f.launcher.asked)
}

func TestReconcileMarksLostAnEgressWhoseProbeExpires(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_hang", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_hang"] = errHangs

	f.run()

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_hang"))
}

func TestReconcileLetsALiveHandlerEndTheEgressOfAGoneRoom(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_alive", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.egress(t, "EG_erroring", "gone", livekit.EgressStatus_EGRESS_ENDING, f.now.Add(-time.Hour))
	f.launcher.answers["EG_erroring"] = psrpc.NewErrorf(psrpc.InvalidArgument, "already ending")

	f.run()

	require.ElementsMatch(t, []string{"EG_alive", "EG_erroring"}, f.launcher.asked)
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_alive"))
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING, f.egressStatus(t, "EG_erroring"))
}

func TestReconcileGivesAStartingEgressTimeToLaunch(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_young", "gone", livekit.EgressStatus_EGRESS_STARTING, f.now.Add(-30*time.Second))
	f.egress(t, "EG_old", "gone", livekit.EgressStatus_EGRESS_STARTING, f.now.Add(-egressStartingGrace-time.Second))
	f.launcher.answers["EG_young"] = psrpc.ErrRequestTimedOut
	f.launcher.answers["EG_old"] = psrpc.ErrRequestTimedOut

	f.run()

	require.Equal(t, []string{"EG_old"}, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, f.egressStatus(t, "EG_young"))
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_old"))
}

func TestReconcileSkipsWebEgressesAndEndedRows(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_web", "", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.egress(t, "EG_done", "gone", livekit.EgressStatus_EGRESS_COMPLETE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_web"] = psrpc.ErrRequestTimedOut
	f.launcher.answers["EG_done"] = psrpc.ErrRequestTimedOut

	f.run()

	require.Empty(t, f.launcher.asked)
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
}

func TestReconcileRunsOnOneNodeAtATime(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = errGated
	f.launcher.entered = make(chan string)
	f.launcher.release = make(chan struct{})

	// node A holds the lock while its probe is in flight...
	done := make(chan struct{})
	go func() { f.run(); close(done) }()
	require.Equal(t, "EG_lost", <-f.launcher.entered)
	// ...and node B, in phase with it, finds the lock taken and leaves
	f.run()
	close(f.launcher.release)
	<-done

	require.Equal(t, []string{"EG_lost"}, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_lost"))
	require.Equal(t, 1, f.tel.EgressEndedCallCount(), "one egress_ended, not one per node")
	f.lockIsFree(t)
}

func TestReconcileSkipsThePassWhileAnotherNodeHoldsTheLock(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut
	lock, err := redislock.New(f.store.rc).Obtain(f.ctx, reconcileLockKey, time.Minute, nil)
	require.NoError(t, err)

	f.run()
	require.Empty(t, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_lost"))

	require.NoError(t, lock.Release(f.ctx))
	f.run()
	require.Equal(t, []string{"EG_lost"}, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_lost"))
}

func TestReconcileLeavesAnEgressWhoseRowMovedDuringTheProbeAlone(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_late", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_late"] = psrpc.ErrRequestTimedOut
	// the handler took the stop request but answered late: it is stopping, not dead
	f.launcher.beforeAnswer = func(id string) {
		require.NoError(t, f.store.UpdateEgress(f.ctx, &livekit.EgressInfo{
			EgressId: id, RoomName: "gone", Status: livekit.EgressStatus_EGRESS_ENDING, UpdatedAt: f.now.UnixNano(),
		}))
	}

	f.run()

	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING, f.egressStatus(t, "EG_late"))
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
}

func TestReconcileLeavesAnEgressThatEndedDuringTheProbeAlone(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_racing", "gone", livekit.EgressStatus_EGRESS_ENDING, f.now.Add(-time.Hour))
	f.launcher.answers["EG_racing"] = psrpc.ErrRequestTimedOut
	// the handler was only slow: it uploads, completes its row and exits while the probe waits
	f.launcher.beforeAnswer = func(id string) {
		require.NoError(t, f.store.UpdateEgress(f.ctx, &livekit.EgressInfo{
			EgressId: id, RoomName: "gone", Status: livekit.EgressStatus_EGRESS_COMPLETE, EndedAt: f.now.UnixNano(),
		}))
	}

	f.run()

	require.Equal(t, livekit.EgressStatus_EGRESS_COMPLETE, f.egressStatus(t, "EG_racing"))
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
}

func TestReconcileStopsWhenItsContextIsCancelled(t *testing.T) {
	f := newReconcileFixture(t)
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithCancel(f.ctx)
	f.egress(t, "EG_first", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.egress(t, "EG_second", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.ingress(t, "IN_gone", "gone", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))
	f.launcher.answers["EG_first"] = errHangs
	f.launcher.answers["EG_second"] = errHangs
	// the server shuts down while the first probe is in flight
	f.launcher.beforeAnswer = func(string) { cancel() }

	f.run()

	require.Len(t, f.launcher.asked, 1, "no further probes once the context is done")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_first"), "an unanswered probe under a cancelled context proves nothing")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_second"))
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_gone").Status)
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
	f.lockIsFree(t) // not kept until the TTL by the cancelled context
}

func TestReconcileStopsWhenItsBudgetRunsOut(t *testing.T) {
	f := newReconcileFixture(t)
	previous := reconcilePassBudget
	reconcilePassBudget = 50 * time.Millisecond
	t.Cleanup(func() { reconcilePassBudget = previous })
	f.egress(t, "EG_first", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.egress(t, "EG_second", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.ingress(t, "IN_gone", "gone", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))
	f.launcher.answers["EG_first"] = errHangs
	f.launcher.answers["EG_second"] = errHangs

	f.run()

	require.Len(t, f.launcher.asked, 1, "the rest waits for the next tick")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_first"), "a probe cut short by the budget proves nothing")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_second"))
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_gone").Status, "the ingress pass waits too")
	require.Equal(t, 0, f.tel.EgressEndedCallCount())
	require.Equal(t, 0, f.tel.IngressEndedCallCount())
	f.lockIsFree(t)
}

func TestReconcileSkipsThePassWhenTheLockCannotBeTaken(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut
	// something else sits under the lock key, so taking it fails with a Redis error, not "held"
	require.NoError(t, f.store.rc.HSet(f.ctx, reconcileLockKey, "not", "a lock").Err())

	f.run()

	require.Empty(t, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_lost"))
}

func TestReconcileEgressesFailsClosedOnStoreErrors(t *testing.T) {
	for _, failing := range []string{"ListEgress", "LoadRoom"} {
		t.Run(failing, func(t *testing.T) {
			f := newReconcileFixture(t)
			f.egress(t, "EG_lost", "gone", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
			f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut

			reconcileEgresses(f.ctx, &failingStore{reconcileStore: f.store, failing: failing}, f.launcher, f.tel)

			require.Empty(t, f.launcher.asked, "a Redis hiccup is no reason to probe anything")
			require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_lost"))
			require.Equal(t, 0, f.tel.EgressEndedCallCount())
		})
	}
}

func TestReconcileIngressesFailsClosedOnStoreErrors(t *testing.T) {
	cases := []struct{ failing, room string }{
		{"ListIngress", "gone"},
		{"LoadRoom", "gone"},
		{"LoadRoom", "live"},
		{"HasParticipant", "live"},
	}
	for _, c := range cases {
		t.Run(c.failing+" with room "+c.room, func(t *testing.T) {
			f := newReconcileFixture(t)
			f.room(t, "live", "someone-else") // "drone" is not in it, so the session would be reset
			f.ingress(t, "IN_x", c.room, "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))

			reconcileIngresses(f.ctx, &failingStore{reconcileStore: f.store, failing: c.failing}, f.tel)

			require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_x").Status, "a Redis hiccup is no reason to reset anything")
			require.Equal(t, 0, f.tel.IngressEndedCallCount())
		})
	}
}

func TestReconcileJudgesRoomsBySidWhenTheNameIsReused(t *testing.T) {
	f := newReconcileFixture(t)
	f.room(t, "meeting", "drone") // the current "meeting" room is RM_meeting
	f.egressInRoom(t, "EG_current", "meeting", "RM_meeting")
	f.egressInRoom(t, "EG_previous", "meeting", "RM_older-meeting")
	f.egressInRoom(t, "EG_unknown", "meeting", "")
	// same for an ingress session left over from an earlier room of the same name
	f.ingress(t, "IN_previous", "meeting", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))
	previous := f.ingressState(t, "IN_previous")
	previous.RoomId = "RM_older-meeting"
	previous.UpdatedAt++ // a later write than the one just stored, or the store keeps the old state
	require.NoError(t, f.store.UpdateIngressState(f.ctx, "IN_previous", previous))
	f.ingress(t, "IN_current", "meeting", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))

	f.run()

	require.Equal(t, []string{"EG_previous"}, f.launcher.asked)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_previous"))
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_current"))
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_unknown"), "no sid recorded: the name decides")
	require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, f.ingressState(t, "IN_previous").Status)
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_current").Status)
}

func TestReconcileResetsTheStateOfAnIngressWhoseRoomIsGone(t *testing.T) {
	f := newReconcileFixture(t)
	started := f.now.Add(-10 * time.Minute)
	f.ingress(t, "IN_gone", "gone", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, started)

	f.run()

	state := f.ingressState(t, "IN_gone")
	require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, state.Status)
	require.Equal(t, started.UnixNano(), state.StartedAt, "the session start is kept, so a newer session still wins")
	require.Equal(t, f.now.UnixNano(), state.EndedAt)
	require.Equal(t, "res-IN_gone", state.ResourceId)
	require.Empty(t, state.Tracks)
	require.Empty(t, state.Error)
	require.Equal(t, 1, f.tel.IngressEndedCallCount())
}

func TestReconcileResetsTheStateOfAnIngressWhoseParticipantLeft(t *testing.T) {
	f := newReconcileFixture(t)
	f.room(t, "live", "someone-else")
	f.ingress(t, "IN_left", "live", "drone", livekit.IngressState_ENDPOINT_BUFFERING, f.now.Add(-10*time.Minute))

	f.run()

	require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, f.ingressState(t, "IN_left").Status)
}

func TestReconcileLeavesLiveYoungAndInactiveIngressesAlone(t *testing.T) {
	f := newReconcileFixture(t)
	f.room(t, "live", "drone")
	f.ingress(t, "IN_live", "live", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))
	f.ingress(t, "IN_young", "gone", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Second))
	f.ingress(t, "IN_idle", "gone", "drone", livekit.IngressState_ENDPOINT_INACTIVE, f.now.Add(-10*time.Minute))

	f.run()

	require.Equal(t, 0, f.tel.IngressEndedCallCount())
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_live").Status)
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_young").Status)
}

// movingStore is the shared store as seen by a pass that raced the ingress
// service: between the listing and the reset, the session reported a newer
// state, which the store then keeps over the reset without an error.
type movingStore struct {
	reconcileStore
	t *testing.T
}

func (s *movingStore) UpdateIngressState(ctx context.Context, ingressId string, state *livekit.IngressState) error {
	newer := proto.Clone(state).(*livekit.IngressState)
	newer.Status = livekit.IngressState_ENDPOINT_PUBLISHING
	newer.EndedAt = 0
	newer.UpdatedAt = state.UpdatedAt + 1
	require.NoError(s.t, s.reconcileStore.UpdateIngressState(ctx, ingressId, newer))
	return s.reconcileStore.UpdateIngressState(ctx, ingressId, state)
}

func TestReconcileDoesNotReportAnIngressWhoseStateMovedOn(t *testing.T) {
	f := newReconcileFixture(t)
	f.ingress(t, "IN_moving", "gone", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))

	reconcileIngresses(f.ctx, &movingStore{reconcileStore: f.store, t: t}, f.tel)

	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_moving").Status)
	require.Equal(t, 0, f.tel.IngressEndedCallCount(), "nothing was reset, so nothing ended")
}

func TestReconcileLeavesIngressesItCannotJudgeAlone(t *testing.T) {
	f := newReconcileFixture(t)
	// never started a session: no state row at all
	require.NoError(t, f.store.StoreIngress(f.ctx, &livekit.IngressInfo{
		IngressId: "IN_idle", Name: "IN_idle", StreamKey: "key-IN_idle", InputType: livekit.IngressInput_RTMP_INPUT,
		RoomName: "gone", ParticipantIdentity: "drone",
	}))
	// no room name: it is chosen when the publisher connects
	f.ingress(t, "IN_roomless", "", "drone", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))
	// a live room but no identity recorded: nothing to look for in it
	f.room(t, "live", "someone")
	f.ingress(t, "IN_anonymous", "live", "", livekit.IngressState_ENDPOINT_PUBLISHING, f.now.Add(-10*time.Minute))

	f.run()

	require.Equal(t, 0, f.tel.IngressEndedCallCount())
	idle := f.ingressState(t, "IN_idle")
	require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, idle.Status)
	require.Zero(t, idle.EndedAt, "nothing was reset")
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_roomless").Status)
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, f.ingressState(t, "IN_anonymous").Status)
}

func TestReconcileDoesNothingWithoutASharedStore(t *testing.T) {
	f := newReconcileFixture(t)
	reconcileEntities(f.ctx, NewLocalStore(), f.launcher, f.tel)
	require.Empty(t, f.launcher.asked)
}

func TestEgressUnreachable(t *testing.T) {
	require.True(t, egressUnreachable(psrpc.ErrRequestTimedOut))
	require.True(t, egressUnreachable(psrpc.ErrNoResponse))
	require.True(t, egressUnreachable(context.DeadlineExceeded))
	require.False(t, egressUnreachable(psrpc.NewErrorf(psrpc.InvalidArgument, "UpdateStream called on non-streaming egress")))
	require.False(t, egressUnreachable(psrpc.NewErrorf(psrpc.NotFound, "egress not found")))
	require.False(t, egressUnreachable(ErrEgressNotConnected))
	require.False(t, egressUnreachable(errors.New("something else")))
	require.False(t, egressUnreachable(nil))
}

func TestMarkEgressLostLeavesTheGivenRowUntouched(t *testing.T) {
	info := &livekit.EgressInfo{EgressId: "EG_x", Status: livekit.EgressStatus_EGRESS_ACTIVE}
	failing := func(context.Context, *livekit.EgressInfo) error { return errors.New("redis is down") }
	_, err := markEgressLost(context.Background(), failing, info, time.Now())
	require.Error(t, err)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, info.Status)
	require.Empty(t, info.Error)

	var stored *livekit.EgressInfo
	storing := func(_ context.Context, i *livekit.EgressInfo) error { stored = i; return nil }
	lost, err := markEgressLost(context.Background(), storing, info, time.Unix(0, 42))
	require.NoError(t, err)
	require.Same(t, stored, lost)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, lost.Status)
	require.Equal(t, egressLostError, lost.Error)
	require.EqualValues(t, 42, lost.EndedAt)
	require.EqualValues(t, 42, lost.UpdatedAt)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, info.Status)
}

func recordingCtx() context.Context {
	return WithGrants(context.Background(), &auth.ClaimGrants{Video: &auth.VideoGrant{RoomRecord: true}}, "")
}

func TestStopEgressMarksLostAnEgressNobodyAnswersFor(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "room", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut
	svc := &EgressService{launcher: f.launcher, io: &ioStub{store: f.store}}

	info, err := svc.StopEgress(recordingCtx(), &livekit.StopEgressRequest{EgressId: "EG_lost"})

	require.NoError(t, err)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	require.Equal(t, egressLostError, info.Error)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, f.egressStatus(t, "EG_lost"))
}

func TestStopEgressReportsTheHandlerErrorWhenTheEgressIsNotLost(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_busy", "room", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_busy"] = psrpc.NewErrorf(psrpc.Internal, "handler says no")
	svc := &EgressService{launcher: f.launcher, io: &ioStub{store: f.store}}

	_, err := svc.StopEgress(recordingCtx(), &livekit.StopEgressRequest{EgressId: "EG_busy"})

	require.ErrorContains(t, err, "handler says no")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_busy"))
}

func TestStopEgressReportsTheTimeoutWhenMarkingLostFails(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "room", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut
	svc := &EgressService{launcher: f.launcher, io: &ioStub{store: f.store, updateErr: errors.New("redis is down")}}

	_, err := svc.StopEgress(recordingCtx(), &livekit.StopEgressRequest{EgressId: "EG_lost"})

	require.ErrorIs(t, err, psrpc.ErrRequestTimedOut, "the transport error, not a made-up precondition failure")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_lost"))
}

func TestStopEgressRefusesToEndAnEgressThatAlreadyEnded(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_done", "room", livekit.EgressStatus_EGRESS_COMPLETE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_done"] = psrpc.ErrRequestTimedOut // its handler is long gone, rightly so
	io := &ioStub{store: f.store}
	svc := &EgressService{launcher: f.launcher, io: io}

	_, err := svc.StopEgress(recordingCtx(), &livekit.StopEgressRequest{EgressId: "EG_done"})

	var terr twirp.Error
	require.ErrorAs(t, err, &terr)
	require.Equal(t, twirp.FailedPrecondition, terr.Code())
	require.Equal(t, 0, io.updates, "a finished egress is not ended again")
	require.Equal(t, livekit.EgressStatus_EGRESS_COMPLETE, f.egressStatus(t, "EG_done"))
}

func TestStopEgressWithoutAnUpdaterReportsTheHandlerError(t *testing.T) {
	f := newReconcileFixture(t)
	f.egress(t, "EG_lost", "room", livekit.EgressStatus_EGRESS_ACTIVE, f.now.Add(-time.Hour))
	f.launcher.answers["EG_lost"] = psrpc.ErrRequestTimedOut
	svc := &EgressService{launcher: f.launcher, io: &plainIOStub{store: f.store}}

	_, err := svc.StopEgress(recordingCtx(), &livekit.StopEgressRequest{EgressId: "EG_lost"})

	require.ErrorIs(t, err, psrpc.ErrRequestTimedOut, "upstream behaviour when nothing can mark the egress lost")
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, f.egressStatus(t, "EG_lost"))
}
