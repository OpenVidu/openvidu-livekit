package service

import (
	"context"
	"errors"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/livekit-server/pkg/rtc"
	"github.com/livekit/livekit-server/pkg/telemetry"
)

// Egress and ingress are durable entities: their Redis rows live until an API
// call deletes them, and only the egress and ingress services update them. When
// one of those services dies mid-session (a crash, a SIGKILL after the stop
// grace period) its last update never arrives, so an egress row stays
// EGRESS_ACTIVE forever, ListEgress and the dashboards report a recording that
// does not exist and StopEgress times out against a handler that is gone; an
// ingress session likewise stays ENDPOINT_PUBLISHING. Rooms do not have this
// problem: RemoveDeadNodes deletes the rooms of a dead node. It then calls
// this, which does the equivalent for egress rows and ingress state, judging
// them by the rooms they belong to.

const (
	// how long an egress handler is given to answer
	egressReconcileProbeTimeout = 2 * time.Second
	// a row still EGRESS_STARTING may belong to a handler that is launching,
	// and a launching handler does not answer requests
	egressStartingGrace = 2 * time.Minute
	// an ingress session younger than this may not have joined its room yet
	ingressStateGrace = time.Minute
	// every node runs the pass on its own schedule; the lock makes it one at a
	// time, so a lost entity is ended and reported once. Its TTL is the
	// longest a holder that died mid-pass keeps the others out.
	reconcileLockKey = "entity-reconcile-lock"
	reconcileLockTTL = 3 * time.Minute

	egressLostError = "egress lost: no egress handler answers for it"
)

var (
	reconcileNow = time.Now // replaced by tests
	// a pass probes serially, so after a mass crash it could outlive the lock;
	// it stops here instead and leaves the rest to the next tick
	reconcilePassBudget = 2 * time.Minute // shortened by tests
)

// reconcileStore is what reconciliation reads and writes.
type reconcileStore interface {
	LoadRoom(ctx context.Context, roomName livekit.RoomName, includeInternal bool) (*livekit.Room, *livekit.RoomInternal, error)
	HasParticipant(ctx context.Context, roomName livekit.RoomName, identity livekit.ParticipantIdentity) (bool, error)
	EgressStore
	IngressStore
}

// egressUpdater is what the lost-egress fallback of StopEgress needs from the
// IO service. IOClient, the narrowed view the egress service holds, does not
// declare UpdateEgress, but wire binds it to *IOInfoService, which has it.
type egressUpdater interface {
	UpdateEgress(context.Context, *livekit.EgressInfo) (*emptypb.Empty, error)
}

var _ egressUpdater = (*IOInfoService)(nil)

// reconcileEntities runs one pass over the shared store. The in-memory store
// used without Redis has nothing shared to reconcile.
func reconcileEntities(ctx context.Context, store ObjectStore, egress rtc.EgressLauncher, tel telemetry.TelemetryService) {
	rs, ok := store.(*RedisStore)
	if !ok {
		return
	}
	lock, err := redislock.New(rs.rc).Obtain(ctx, reconcileLockKey, reconcileLockTTL, nil)
	if err != nil {
		if !errors.Is(err, redislock.ErrNotObtained) && ctx.Err() == nil {
			logger.Warnw("entity cleanup: could not take the lock", err)
		}
		return // another node is on it
	}
	defer lock.Release(context.WithoutCancel(ctx))
	ctx, cancel := context.WithTimeout(ctx, reconcilePassBudget)
	defer cancel()
	reconcileEgresses(ctx, rs, egress, tel)
	if ctx.Err() != nil {
		return
	}
	reconcileIngresses(ctx, rs, tel)
}

// reconcileEgresses ends the egresses of rooms that no longer exist. A live
// handler is simply asked to stop, which is what it does on its own on losing
// the room; one that does not answer is dead, and its row is marked lost so it
// stops counting as active and the sweeper collects it. Egresses of live rooms
// are left to their handlers, and web egresses have no room to be judged by.
func reconcileEgresses(ctx context.Context, store reconcileStore, egress rtc.EgressLauncher, tel telemetry.TelemetryService) {
	infos, err := store.ListEgress(ctx, "", true)
	if err != nil {
		logger.Warnw("entity cleanup: could not list egresses", err)
		return
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return
		}
		if info.RoomName == "" || (info.Status == livekit.EgressStatus_EGRESS_STARTING &&
			reconcileNow().Sub(time.Unix(0, info.UpdatedAt)) < egressStartingGrace) {
			continue
		}
		if gone, err := roomGone(ctx, store, info.RoomName, info.RoomId); err != nil || !gone {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, egressReconcileProbeTimeout)
		_, err = egress.StopEgress(probeCtx, &livekit.StopEgressRequest{EgressId: info.EgressId})
		// psrpc reports a caller deadline as "canceled", so the probe's own expiry is
		// checked too; and a parent that is done says nothing about the handler
		unreachable := ctx.Err() == nil && (egressUnreachable(err) || errors.Is(probeCtx.Err(), context.DeadlineExceeded))
		cancel()
		if err == nil {
			logger.Infow("entity cleanup: egress of a gone room asked to stop", "egressID", info.EgressId, "roomName", info.RoomName)
		} else if unreachable {
			// the row may have moved on since the listing: ended by its handler or by
			// another node, or written by a handler that is alive but answered late
			current, err := store.LoadEgress(ctx, info.EgressId)
			if err != nil || egressEnded(current) || current.UpdatedAt != info.UpdatedAt {
				continue
			}
			lost, err := markEgressLost(ctx, store.UpdateEgress, current, reconcileNow())
			if err != nil {
				logger.Warnw("entity cleanup: could not mark egress as lost", err, "egressID", info.EgressId)
				continue
			}
			tel.EgressEnded(ctx, lost)
			logger.Infow("entity cleanup: egress lost, marked as failed", "egressID", info.EgressId, "roomName", info.RoomName)
		}
		// any other error: the handler answered, it is alive and will report its own end
	}
}

// reconcileIngresses resets the state of ingress sessions whose room, or
// participant in it, is gone: the ingress service that owned them died before
// reporting their end, and nothing else ever will.
func reconcileIngresses(ctx context.Context, store reconcileStore, tel telemetry.TelemetryService) {
	infos, err := store.ListIngress(ctx, "")
	if err != nil {
		logger.Warnw("entity cleanup: could not list ingresses", err)
		return
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return
		}
		state := info.State
		if state == nil || info.RoomName == "" ||
			(state.Status != livekit.IngressState_ENDPOINT_PUBLISHING && state.Status != livekit.IngressState_ENDPOINT_BUFFERING) ||
			reconcileNow().Sub(time.Unix(0, max(state.StartedAt, state.UpdatedAt))) < ingressStateGrace {
			continue
		}
		gone, err := roomGone(ctx, store, info.RoomName, state.RoomId)
		if err == nil && !gone && info.ParticipantIdentity != "" {
			var present bool
			present, err = store.HasParticipant(ctx, livekit.RoomName(info.RoomName), livekit.ParticipantIdentity(info.ParticipantIdentity))
			gone = !present
		}
		if err != nil || !gone {
			continue
		}
		ended := proto.Clone(state).(*livekit.IngressState)
		ended.Status = livekit.IngressState_ENDPOINT_INACTIVE
		ended.EndedAt = reconcileNow().UnixNano()
		ended.UpdatedAt = ended.EndedAt
		ended.Tracks = nil
		if err := store.UpdateIngressState(ctx, info.IngressId, ended); err != nil {
			logger.Warnw("entity cleanup: could not reset ingress state", err, "ingressID", info.IngressId)
			continue
		}
		// the store keeps a newer state without a word: then the session moved on
		// since it was listed, so it is alive and nothing happened. Should this read
		// fail after a stored reset, the row is right and only the event is missing,
		// which is the better way round: the analytics fixer recovers that from Redis
		if current, err := store.LoadIngress(ctx, info.IngressId); err != nil || current.State == nil || current.State.UpdatedAt != ended.UpdatedAt {
			continue
		}
		info.State = ended
		tel.IngressEnded(ctx, info)
		logger.Infow("entity cleanup: ingress session lost, state reset to inactive", "ingressID", info.IngressId, "roomName", info.RoomName)
	}
}

// roomGone reports whether the room an entity belongs to no longer exists.
// Room names are reused, so a live room under the same name with another sid
// is somebody else's room, and the entity's is gone all the same.
func roomGone(ctx context.Context, store reconcileStore, name, sid string) (bool, error) {
	room, _, err := store.LoadRoom(ctx, livekit.RoomName(name), false)
	if errors.Is(err, ErrRoomNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return sid != "" && room.Sid != sid, nil
}

// egressUnreachable reports whether an egress handler request failed because
// nothing answered it, as opposed to the handler answering with an error.
func egressUnreachable(err error) bool {
	var perr psrpc.Error
	if errors.As(err, &perr) {
		return perr.Code() == psrpc.DeadlineExceeded || perr.Code() == psrpc.Unavailable
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// markEgressLost ends an egress whose handler is gone: failed, with an end
// time, so the row stops counting as active and the sweeper collects it. The
// row given is left as it is; the ended copy is returned once it is stored.
func markEgressLost(ctx context.Context, update func(context.Context, *livekit.EgressInfo) error, info *livekit.EgressInfo, now time.Time) (*livekit.EgressInfo, error) {
	lost := proto.Clone(info).(*livekit.EgressInfo)
	lost.Status = livekit.EgressStatus_EGRESS_FAILED
	lost.Error = egressLostError
	lost.EndedAt = now.UnixNano()
	lost.UpdatedAt = lost.EndedAt
	if err := update(ctx, lost); err != nil {
		return nil, err
	}
	return lost, nil
}

// egressEnded reports whether an egress row is in a terminal status.
func egressEnded(info *livekit.EgressInfo) bool {
	return int32(info.Status) >= int32(livekit.EgressStatus_EGRESS_COMPLETE)
}
