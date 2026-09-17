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
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/service"
)

// seedIngresses stores `total` ingresses the way the store keeps them: info in the ingress hash and
// membership of one of ten rooms. Every third one also gets a state, with StartedAt set to its index
// plus one; the others have no state key at all, as an ingress stored before states existed.
func seedIngresses(t testing.TB, rc *redis.Client, total int) map[string]int64 {
	ctx := context.Background()
	startedAt := make(map[string]int64)
	pp := rc.Pipeline()
	for i := 0; i < total; i++ {
		ingressID := fmt.Sprintf("IN_%04d", i)
		roomName := fmt.Sprintf("room-%d", i%10)
		data, err := proto.Marshal(&livekit.IngressInfo{
			IngressId: ingressID,
			StreamKey: fmt.Sprintf("key-%d", i),
			RoomName:  roomName,
		})
		require.NoError(t, err)
		pp.HSet(ctx, service.IngressKey, ingressID, data)
		pp.SAdd(ctx, service.RoomIngressPrefix+roomName, ingressID)
		if i%3 == 0 {
			state, err := proto.Marshal(&livekit.IngressState{
				Status:    livekit.IngressState_ENDPOINT_PUBLISHING,
				StartedAt: int64(i + 1),
			})
			require.NoError(t, err)
			pp.Set(ctx, service.IngressStatePrefix+ingressID, state, 0)
			startedAt[ingressID] = int64(i + 1)
		}
	}
	_, err := pp.Exec(ctx)
	require.NoError(t, err)
	return startedAt
}

// requireIngressesListedOnce checks that every expected ingress is listed exactly once, with its state
// when it has one and no state otherwise.
func requireIngressesListedOnce(t testing.TB, infos []*livekit.IngressInfo, expected int, startedAt map[string]int64) {
	require.Len(t, infos, expected)
	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		_, dup := seen[info.IngressId]
		require.False(t, dup, "ingress %s listed twice", info.IngressId)
		seen[info.IngressId] = struct{}{}
		if want, ok := startedAt[info.IngressId]; ok {
			require.NotNil(t, info.State, "ingress %s lost its state", info.IngressId)
			require.Equal(t, want, info.State.StartedAt, "ingress %s got another ingress' state", info.IngressId)
		} else {
			require.Nil(t, info.State, "ingress %s has a state it never stored", info.IngressId)
		}
	}
}

func TestListIngressWithoutRoomScansTheHash(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)
	ctx := context.Background()

	// more ingresses than two HSCAN chunks
	const total = 1207
	startedAt := seedIngresses(t, rc, total)

	all, err := rs.ListIngress(ctx, "")
	require.NoError(t, err)
	requireIngressesListedOnce(t, all, total, startedAt)

	// the listing by room takes the other path and must agree with it
	byRoom, err := rs.ListIngress(ctx, "room-3")
	require.NoError(t, err)
	inRoom := 0
	for i := 0; i < total; i++ {
		if i%10 == 3 {
			inRoom++
		}
	}
	requireIngressesListedOnce(t, byRoom, inRoom, startedAt)
	for _, info := range byRoom {
		require.Equal(t, "room-3", info.RoomName)
	}
}

func TestListIngressWithoutRoomOnEmptyHash(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)

	infos, err := rs.ListIngress(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, infos)
}
