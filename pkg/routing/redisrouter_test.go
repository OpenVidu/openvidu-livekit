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

package routing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc/pkg/bus/redisbus"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/routing/selector"
)

func TestClassifyKeepalivePing(t *testing.T) {
	interval := 2 * time.Second
	cases := []struct {
		name     string
		age      time.Duration
		localLag time.Duration
		want     keepaliveVerdict
	}{
		{"fresh ping, idle node", time.Second, 0, keepaliveOnTime},
		{"fresh ping, lagging node", time.Second, 3 * time.Second, keepaliveOnTime},
		{"old ping, node on time: redis delayed it", 5 * time.Second, 10 * time.Millisecond, keepaliveLateRedis},
		{"old ping, lag exactly at the threshold", 5 * time.Second, interval / 2, keepaliveLateRedis},
		{"old ping, node lagging: node is starved", 5 * time.Second, 1500 * time.Millisecond, keepaliveLateNode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, classifyKeepalivePing(c.age, c.localLag, interval))
		})
	}
}

type noopCleanup struct{}

func (noopCleanup) LockRoom(context.Context, livekit.RoomName, time.Duration) (string, error) {
	return "token", nil
}
func (noopCleanup) UnlockRoom(context.Context, livekit.RoomName, string) error { return nil }
func (noopCleanup) PublicDeleteRoom(context.Context, livekit.RoomName) error   { return nil }
func (noopCleanup) ReconcileEntities(context.Context)                          {}

func TestRemoveDeadNodesThreshold(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	ctx := context.Background()

	self, err := NewLocalNodeFromNodeProto(&livekit.Node{Id: "self"})
	require.NoError(t, err)
	router := NewRedisRouter(NewLocalRouter(self, nil, nil, config.DefaultNodeStatsConfig), rc, nil)

	seed := func(id string, age time.Duration) {
		data, err := proto.Marshal(&livekit.Node{
			Id:    id,
			State: livekit.NodeState_SERVING,
			Stats: &livekit.NodeStats{UpdatedAt: time.Now().Add(-age).Unix()},
		})
		require.NoError(t, err)
		require.NoError(t, rc.HSet(ctx, NodesKey, id, data).Err())
	}
	// below the timeout: a few seconds late but still fully usable
	seed("slow", time.Duration(selector.AvailableSeconds-5)*time.Second)
	// past the timeout: dead
	seed("dead", time.Duration(selector.AvailableSeconds+1)*time.Second)

	markSteady(router)
	require.NoError(t, router.RemoveDeadNodes(noopCleanup{}))

	require.True(t, rc.HExists(ctx, NodesKey, "slow").Val(), "a node below the timeout must not be removed")
	require.False(t, rc.HExists(ctx, NodesKey, "dead").Val(), "a node past AvailableSeconds must be removed")
}

// markSteady makes the router look like it has written its own entry on time for the last few intervals.
func markSteady(r *RedisRouter) {
	interval := r.nodeStatsConfig.StatsUpdateInterval
	now := time.Now()
	for i := 4; i >= 0; i-- {
		r.recordRegistration(now.Add(-time.Duration(i) * interval))
	}
}

func TestRegistrationSteady(t *testing.T) {
	self, err := NewLocalNodeFromNodeProto(&livekit.Node{Id: "self"})
	require.NoError(t, err)
	interval := config.DefaultNodeStatsConfig.StatsUpdateInterval
	start := time.Now()
	at := func(intervals float64) time.Time {
		return start.Add(time.Duration(intervals * float64(interval)))
	}

	cases := []struct {
		name   string
		writes []float64 // when the node wrote its own entry, in stats intervals
		now    float64
		steady bool
	}{
		{"never registered (a node that is starting)", nil, 0, false},
		{"just registered", []float64{0, 1, 2}, 2, false},
		{"registering on time for three intervals", []float64{0, 1, 2, 3}, 3, true},
		{"own entry stale now (Redis down or slow)", []float64{0, 1, 2, 3, 4}, 7, false},
		{"Redis just came back", []float64{0, 1, 2, 3, 8, 9}, 9, false},
		{"back for three intervals", []float64{0, 1, 2, 3, 8, 9, 10, 11}, 11, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			router := NewRedisRouter(NewLocalRouter(self, nil, nil, config.DefaultNodeStatsConfig), nil, nil)
			for _, w := range c.writes {
				router.recordRegistration(at(w))
			}
			require.Equal(t, c.steady, router.registrationSteady(at(c.now)))
		})
	}
}

// Right after Redis comes back every node looks stale, this one included: its cleanup must leave the
// others alone until its own registration is steady again, and remove a dead node from then on.
func TestRemoveDeadNodesWaitsForSteadyRegistration(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	ctx := context.Background()

	self, err := NewLocalNodeFromNodeProto(&livekit.Node{Id: "self"})
	require.NoError(t, err)
	newRouter := func() *RedisRouter {
		return NewRedisRouter(NewLocalRouter(self, nil, nil, config.DefaultNodeStatsConfig), rc, nil)
	}

	data, err := proto.Marshal(&livekit.Node{
		Id:    "stale",
		State: livekit.NodeState_SERVING,
		Stats: &livekit.NodeStats{UpdatedAt: time.Now().Add(-time.Duration(selector.AvailableSeconds+1) * time.Second).Unix()},
	})
	require.NoError(t, err)
	require.NoError(t, rc.HSet(ctx, NodesKey, "stale", data).Err())

	// own writes stopped for a while and resumed one interval ago
	interval := config.DefaultNodeStatsConfig.StatsUpdateInterval
	now := time.Now()
	cameBack := newRouter()
	for _, ago := range []time.Duration{10, 1, 0} {
		cameBack.recordRegistration(now.Add(-ago * interval))
	}
	require.NoError(t, cameBack.RemoveDeadNodes(noopCleanup{}))
	require.True(t, rc.HExists(ctx, NodesKey, "stale").Val(), "a stale node must stay while this node's registration is not steady")

	steady := newRouter()
	markSteady(steady)
	require.NoError(t, steady.RemoveDeadNodes(noopCleanup{}))
	require.False(t, rc.HExists(ctx, NodesKey, "stale").Val(), "a stale node must be removed once this node's registration is steady")
}

// freshStatsNode is a node whose stats refresh always succeeds.
type freshStatsNode struct {
	*LocalNodeImpl
}

func (n freshStatsNode) UpdateNodeStats() bool {
	n.SetStats(&livekit.NodeStats{UpdatedAt: time.Now().Unix()})
	return true
}

// The keepalive of a running router survives a Redis restart, which drops its connections and subscriptions:
// the router writes its own entry again once Redis is back, and its registration is steady three intervals
// after that, so its cleanups resume.
func TestRegistrationSteadyThroughRedisRestart(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr(), DialTimeout: 200 * time.Millisecond,
		ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = rc.Close() })
	kps, err := rpc.NewKeepalivePubSub(rpc.NewClientParams(rpc.PSRPCConfig{}, redisbus.New(rc), logger.GetLogger(), nil))
	require.NoError(t, err)
	node, err := NewLocalNodeFromNodeProto(&livekit.Node{Id: "self", Stats: &livekit.NodeStats{UpdatedAt: time.Now().Unix()}})
	require.NoError(t, err)
	statsConfig := config.DefaultNodeStatsConfig
	statsConfig.StatsUpdateInterval = 200 * time.Millisecond
	router := NewRedisRouter(NewLocalRouter(freshStatsNode{node}, nil, nil, statsConfig), rc, kps)
	require.NoError(t, router.RegisterNode())
	require.NoError(t, router.Start())
	t.Cleanup(router.Stop)

	steady := func() bool { return router.registrationSteady(time.Now()) }
	require.Eventually(t, steady, 5*time.Second, 10*time.Millisecond)

	mr.Close()
	require.Eventually(t, func() bool { return !steady() }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(2 * time.Second)
	require.Never(t, steady, time.Second, 10*time.Millisecond, "no registration while Redis is down")

	require.NoError(t, mr.Restart())
	back := time.Now()
	require.Eventually(t, steady, 10*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, time.Since(back), 3*statsConfig.StatsUpdateInterval)
}
