// Copyright 2023 LiveKit, Inc.
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

package routing

import (
	"bytes"
	"context"
	"runtime/pprof"
	"time"

	"github.com/pkg/errors"
	"github.com/redis/go-redis/v9"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/livekit-server/pkg/routing/selector"

	// BEGIN OPENVIDU BLOCK
	"github.com/openvidu/openvidu-livekit/openvidu/customrouting"
	// END OPENVIDU BLOCK
)

const (
	// hash of node_id => Node proto
	NodesKey = "nodes"

	// hash of room_name => node_id
	NodeRoomKey = "room_node_map"
)

var _ Router = (*RedisRouter)(nil)

// RedisRouter uses Redis pub/sub to route signaling messages across different nodes
// It relies on the RTC node to be the primary driver of the participant connection.
// Because
type RedisRouter struct {
	*LocalRouter

	rc        redis.UniversalClient
	kps       rpc.KeepalivePubSub
	ctx       context.Context
	isStarted atomic.Bool

	cancel func()
}

func NewRedisRouter(lr *LocalRouter, rc redis.UniversalClient, kps rpc.KeepalivePubSub) *RedisRouter {
	rr := &RedisRouter{
		LocalRouter: lr,
		rc:          rc,
		kps:         kps,
	}
	rr.ctx, rr.cancel = context.WithCancel(context.Background())
	return rr
}

func (r *RedisRouter) RegisterNode() error {
	data, err := proto.Marshal(r.currentNode.Clone())
	if err != nil {
		return err
	}
	if err := r.rc.HSet(r.ctx, NodesKey, string(r.currentNode.NodeID()), data).Err(); err != nil {
		return errors.Wrap(err, "could not register node")
	}
	return nil
}

func (r *RedisRouter) UnregisterNode() error {
	// could be called after Stop(), so we'd want to use an unrelated context
	return r.rc.HDel(context.Background(), NodesKey, string(r.currentNode.NodeID())).Err()
}

// BEGIN OPENVIDU BLOCK
func (r *RedisRouter) RemoveDeadNodes(customCleanup CustomCleanup) error {

	logger.Debugw("redis cleanup: removing dead nodes...")

	nodes, err := r.ListNodes()
	if err != nil {
		return err
	}

	logger.Debugw("redis cleanup: listed nodes", "nodes", nodes)

	for _, n := range nodes {
		if !selector.IsAvailable(n) {

			logger.Infow("redis cleanup: dead node detected", "node", n.Id)

			// 1. List all rooms of the dead node
			roomNames, err := customrouting.ListRoomsForNode(context.Background(), r.rc, n.Id)
			if err != nil {
				logger.Errorw("redis cleanup: failed to list rooms for node", err, "node", n.Id)
				continue
			}
			logger.Infow("redis cleanup: rooms for node", "node", n.Id, "rooms", roomNames)

			// 2. Define the function to unlock the room after exiting the cleanup loop
			unlockRoom := func(roomName string, token string) {
				if err := customCleanup.UnlockRoom(context.Background(), livekit.RoomName(roomName), token); err != nil {
					logger.Errorw("redis cleanup: failed to unlock room", err, "room", roomName)
				} else {
					logger.Infow("redis cleanup: room unlocked", "room", roomName)
				}
			}

			for _, roomName := range roomNames {

				// 3. Lock each room to avoid other process re-creating it while it's being cleaned up
				token, err := customCleanup.LockRoom(context.Background(), livekit.RoomName(roomName), 10*time.Second)
				if err != nil {
					logger.Errorw("redis cleanup: failed to lock room", err, "room", roomName)
				} else {
					logger.Infow("redis cleanup: room locked", "room", roomName)

					// 4. Delete the collection of rooms for the node
					customrouting.UnregisterRoomFromNode(r.ctx, r.rc, string(roomName), n.Id)
					logger.Infow("redis cleanup: room unregistered from node", "room", roomName, "node", n.Id)

					// 5. Check if the room has not been re-created in other node
					newNode, err := r.GetNodeForRoom(context.Background(), livekit.RoomName(roomName))
					if err != nil {
						logger.Errorw("redis cleanup: failed to get node for room", err, "room", roomName)
						unlockRoom(roomName, token)
						continue
					}
					if newNode.Id != n.Id {
						logger.Infow("redis cleanup: room already recreated in another node", "room", roomName, "newNode", newNode.Id)
						unlockRoom(roomName, token)
						continue
					}

					// 6. Call RoomManager#deleteRoom to delete every entity related to the room from Redis
					if err := customCleanup.PublicDeleteRoom(context.Background(), livekit.RoomName(roomName)); err != nil {
						logger.Errorw("redis cleanup: failed to delete room", err, "room", roomName)
					} else {
						logger.Infow("redis cleanup: room deleted", "room", roomName)
					}
					unlockRoom(roomName, token)
				}
			}

			// 7. Finally clean the node from the livekit original collection
			if err := r.rc.HDel(context.Background(), NodesKey, n.Id).Err(); err != nil {
				logger.Errorw("redis cleanup: failed to delete node", err, "node", n.Id)
				continue
			}
		} else {
			logger.Debugw("redis cleanup: node is alive", "node", n.Id)
		}
	}
	return nil
}

// END OPENVIDU BLOCK

// GetNodeForRoom finds the node where the room is hosted at
func (r *RedisRouter) GetNodeForRoom(_ context.Context, roomName livekit.RoomName) (*livekit.Node, error) {
	nodeID, err := r.rc.HGet(r.ctx, NodeRoomKey, string(roomName)).Result()
	if err == redis.Nil {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, errors.Wrap(err, "could not get node for room")
	}

	return r.GetNode(livekit.NodeID(nodeID))
}

func (r *RedisRouter) SetNodeForRoom(_ context.Context, roomName livekit.RoomName, nodeID livekit.NodeID) error {

	// BEGIN OPENVIDU BLOCK
	customrouting.RegisterRoomInNode(r.ctx, r.rc, string(roomName), string(nodeID))
	// END OPENVIDU BLOCK

	return r.rc.HSet(r.ctx, NodeRoomKey, string(roomName), string(nodeID)).Err()
}

func (r *RedisRouter) ClearRoomState(_ context.Context, roomName livekit.RoomName) error {

	// BEGIN OPENVIDU BLOCK
	nodeID, _ := r.rc.HGet(context.Background(), NodeRoomKey, string(roomName)).Result()
	if nodeID != "" {
		customrouting.UnregisterRoomFromNode(context.Background(), r.rc, string(roomName), nodeID)
	}
	// END OPENVIDU BLOCK

	if err := r.rc.HDel(context.Background(), NodeRoomKey, string(roomName)).Err(); err != nil {
		return errors.Wrap(err, "could not clear room state")
	}
	return nil
}

func (r *RedisRouter) GetNode(nodeID livekit.NodeID) (*livekit.Node, error) {
	data, err := r.rc.HGet(r.ctx, NodesKey, string(nodeID)).Result()
	if err == redis.Nil {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	n := livekit.Node{}
	if err = proto.Unmarshal([]byte(data), &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *RedisRouter) ListNodes() ([]*livekit.Node, error) {
	items, err := r.rc.HVals(r.ctx, NodesKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "could not list nodes")
	}
	nodes := make([]*livekit.Node, 0, len(items))
	for _, item := range items {
		n := livekit.Node{}
		if err := proto.Unmarshal([]byte(item), &n); err != nil {
			return nil, err
		}
		nodes = append(nodes, &n)
	}
	return nodes, nil
}

func (r *RedisRouter) CreateRoom(ctx context.Context, req *livekit.CreateRoomRequest) (res *livekit.Room, err error) {
	rtcNode, err := r.GetNodeForRoom(ctx, livekit.RoomName(req.Name))
	if err != nil {
		return
	}

	return r.CreateRoomWithNodeID(ctx, req, livekit.NodeID(rtcNode.Id))
}

// StartParticipantSignal signal connection sets up paths to the RTC node, and starts to route messages to that message queue
func (r *RedisRouter) StartParticipantSignal(ctx context.Context, roomName livekit.RoomName, pi ParticipantInit) (res StartParticipantSignalResults, err error) {
	rtcNode, err := r.GetNodeForRoom(ctx, roomName)
	if err != nil {
		return
	}

	return r.StartParticipantSignalWithNodeID(ctx, roomName, pi, livekit.NodeID(rtcNode.Id))
}

func (r *RedisRouter) Start() error {
	if r.isStarted.Swap(true) {
		return nil
	}

	workerStarted := make(chan error)
	go r.statsWorker()
	go r.keepaliveWorker(workerStarted)

	// wait until worker is running
	return <-workerStarted
}

func (r *RedisRouter) Drain() {
	r.currentNode.SetState(livekit.NodeState_SHUTTING_DOWN)
	if err := r.RegisterNode(); err != nil {
		logger.Errorw("failed to mark as draining", err, "nodeID", r.currentNode.NodeID())
	}
}

func (r *RedisRouter) Stop() {
	if !r.isStarted.Swap(false) {
		return
	}
	logger.Debugw("stopping RedisRouter")
	_ = r.UnregisterNode()
	r.cancel()
}

// update node stats and cleanup
func (r *RedisRouter) statsWorker() {
	goroutineDumped := false
	for r.ctx.Err() == nil {
		// update periodically
		select {
		case <-time.After(r.nodeStatsConfig.StatsUpdateInterval):
			r.kps.PublishPing(r.ctx, r.currentNode.NodeID(), &rpc.KeepalivePing{Timestamp: time.Now().Unix()})

			delaySeconds := r.currentNode.SecondsSinceNodeStatsUpdate()
			if delaySeconds > r.nodeStatsConfig.StatsMaxDelay.Seconds() {
				if !goroutineDumped {
					goroutineDumped = true
					buf := bytes.NewBuffer(nil)
					_ = pprof.Lookup("goroutine").WriteTo(buf, 2)
					logger.Errorw("status update delayed, possible deadlock", nil,
						"delay", delaySeconds,
						"goroutines", buf.String())
				}
			} else {
				goroutineDumped = false
			}
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *RedisRouter) keepaliveWorker(startedChan chan error) {
	pings, err := r.kps.SubscribePing(r.ctx, r.currentNode.NodeID())
	if err != nil {
		startedChan <- err
		return
	}
	close(startedChan)

	for ping := range pings.Channel() {
		if time.Since(time.Unix(ping.Timestamp, 0)) > r.nodeStatsConfig.StatsUpdateInterval {
			logger.Infow("keep alive too old, skipping", "timestamp", ping.Timestamp)
			continue
		}

		if !r.currentNode.UpdateNodeStats() {
			continue
		}

		// TODO: check stats against config.Limit values
		if err := r.RegisterNode(); err != nil {
			logger.Errorw("could not update node", err)
		}
	}
}
