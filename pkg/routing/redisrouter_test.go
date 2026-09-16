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

	require.NoError(t, router.RemoveDeadNodes(noopCleanup{}))

	require.True(t, rc.HExists(ctx, NodesKey, "slow").Val(), "a node below the timeout must not be removed")
	require.False(t, rc.HExists(ctx, NodesKey, "dead").Val(), "a node past AvailableSeconds must be removed")
}
