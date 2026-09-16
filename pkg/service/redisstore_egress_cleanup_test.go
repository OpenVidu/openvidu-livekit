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

package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/service"
)

// sharedRedisClients returns n clients over one fresh Redis, so that several stores can work on the same data.
func sharedRedisClients(t testing.TB, n int) []*redis.Client {
	addr := runRedis(t)
	clients := make([]*redis.Client, 0, n)
	for i := 0; i < n; i++ {
		cli := redis.NewClient(&redis.Options{Addr: addr})
		t.Cleanup(func() { _ = cli.Close() })
		clients = append(clients, cli)
	}
	return clients
}

// seedEndedEgress stores egresses the way UpdateEgress leaves them once they end: egress info, ended
// marker and room membership. The first `expired` ones ended 25 hours ago, the rest one hour ago.
func seedEndedEgress(t testing.TB, rc *redis.Client, prefix string, expired, fresh int) {
	ctx := context.Background()
	pp := rc.Pipeline()
	for i := 0; i < expired+fresh; i++ {
		egressID := fmt.Sprintf("%s-EG_%d", prefix, i)
		roomName := fmt.Sprintf("%s-room-%d", prefix, i)
		endedAt := time.Now().Add(-25 * time.Hour).UnixNano()
		if i >= expired {
			endedAt = time.Now().Add(-time.Hour).UnixNano()
		}
		info := &livekit.EgressInfo{
			EgressId: egressID,
			RoomName: roomName,
			Status:   livekit.EgressStatus_EGRESS_COMPLETE,
			EndedAt:  endedAt,
		}
		data, err := proto.Marshal(info)
		require.NoError(t, err)
		pp.HSet(ctx, service.EgressKey, egressID, data)
		pp.HSet(ctx, service.EndedEgressKey, egressID, fmt.Sprintf("%s|%d", roomName, endedAt))
		pp.SAdd(ctx, service.RoomEgressPrefix+roomName, egressID)
	}
	_, err := pp.Exec(ctx)
	require.NoError(t, err)
}

func TestCleanEndedEgressInChunks(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)
	ctx := context.Background()

	// more entries than a single HSCAN chunk
	seedEndedEgress(t, rc, "chunks", 1200, 50)

	require.NoError(t, rs.CleanEndedEgress())

	require.EqualValues(t, 50, rc.HLen(ctx, service.EndedEgressKey).Val())
	require.EqualValues(t, 50, rc.HLen(ctx, service.EgressKey).Val())
	require.EqualValues(t, 0, rc.SCard(ctx, service.RoomEgressPrefix+"chunks-room-0").Val())
	require.EqualValues(t, 0, rc.SCard(ctx, service.RoomEgressPrefix+"chunks-room-1199").Val())
	require.EqualValues(t, 1, rc.SCard(ctx, service.RoomEgressPrefix+"chunks-room-1200").Val())

	// a second cleanup finds nothing left to remove
	require.NoError(t, rs.CleanEndedEgress())
	require.EqualValues(t, 50, rc.HLen(ctx, service.EndedEgressKey).Val())
}

func TestCleanEndedEgressKeepsGoingPastCorruptEntry(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)
	ctx := context.Background()

	seedEndedEgress(t, rc, "corrupt", 20, 0)
	require.NoError(t, rc.HSet(ctx, service.EndedEgressKey, "corrupt-EG_bad", "no-separator").Err())

	err := rs.CleanEndedEgress()
	require.Error(t, err)
	require.Contains(t, err.Error(), "corrupt-EG_bad")

	// every valid expired entry is gone, only the corrupt one remains
	require.EqualValues(t, 1, rc.HLen(ctx, service.EndedEgressKey).Val())
	require.EqualValues(t, 0, rc.HLen(ctx, service.EgressKey).Val())
}

func TestCleanEndedEgressSingleRunner(t *testing.T) {
	clients := sharedRedisClients(t, 2)
	stores := []*service.RedisStore{service.NewRedisStore(clients[0]), service.NewRedisStore(clients[1])}
	ctx := context.Background()

	seedEndedEgress(t, clients[0], "leader", 30, 0)

	cleaned := atomic.NewInt32(0)
	var wg sync.WaitGroup
	for _, rs := range stores {
		wg.Add(1)
		go func(rs *service.RedisStore) {
			defer wg.Done()
			if rs.CleanEndedEgressIfLeader() {
				cleaned.Inc()
			}
		}(rs)
	}
	wg.Wait()

	require.EqualValues(t, 1, cleaned.Load(), "exactly one node must cleanup in a cycle")
	require.EqualValues(t, 0, clients[0].HLen(ctx, service.EndedEgressKey).Val())

	// the lock is kept for the rest of the cycle, so later attempts in the same cycle are skipped too
	require.False(t, stores[0].CleanEndedEgressIfLeader())
	require.False(t, stores[1].CleanEndedEgressIfLeader())
}

func TestListEgressWithoutRoomScansTheHash(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)
	ctx := context.Background()

	// more complete egresses than a single HSCAN chunk, plus one active egress
	seedEndedEgress(t, rc, "list", 0, 1200)
	active := &livekit.EgressInfo{
		EgressId: "list-active",
		RoomName: "list-room-active",
		Status:   livekit.EgressStatus_EGRESS_ACTIVE,
	}
	require.NoError(t, rs.StoreEgress(ctx, active))

	all, err := rs.ListEgress(ctx, "", false)
	require.NoError(t, err)
	require.Len(t, all, 1201)

	activeOnly, err := rs.ListEgress(ctx, "", true)
	require.NoError(t, err)
	require.Len(t, activeOnly, 1)
	require.Equal(t, "list-active", activeOnly[0].EgressId)
}
