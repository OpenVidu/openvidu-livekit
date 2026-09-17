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
// plus one; the others have no state key at all, as an ingress stored before states existed. It
// returns, by ingress id, what a listing must give back for each of them: the info with its state.
func seedIngresses(t testing.TB, rc *redis.Client, total int) map[string]*livekit.IngressInfo {
	ctx := context.Background()
	expected := make(map[string]*livekit.IngressInfo, total)
	pp := rc.Pipeline()
	for i := 0; i < total; i++ {
		ingressID := fmt.Sprintf("IN_%04d", i)
		roomName := fmt.Sprintf("room-%d", i%10)
		info := &livekit.IngressInfo{
			IngressId: ingressID,
			Name:      fmt.Sprintf("ingress-%d", i),
			StreamKey: fmt.Sprintf("key-%d", i),
			RoomName:  roomName,
			InputType: livekit.IngressInput_RTMP_INPUT,
		}
		data, err := proto.Marshal(info)
		require.NoError(t, err)
		pp.HSet(ctx, service.IngressKey, ingressID, data)
		pp.SAdd(ctx, service.RoomIngressPrefix+roomName, ingressID)
		if i%3 == 0 {
			state := &livekit.IngressState{
				Status:     livekit.IngressState_ENDPOINT_PUBLISHING,
				StartedAt:  int64(i + 1),
				ResourceId: fmt.Sprintf("resource-%d", i),
			}
			data, err := proto.Marshal(state)
			require.NoError(t, err)
			pp.Set(ctx, service.IngressStatePrefix+ingressID, data, 0)
			info.State = state
		}
		expected[ingressID] = info
	}
	_, err := pp.Exec(ctx)
	require.NoError(t, err)
	return expected
}

// requireSameIngresses checks that a listing holds exactly the expected ingresses: every one of them
// once, none that was not expected, and each equal field by field, state included, to what was stored
// under its id.
func requireSameIngresses(t testing.TB, expected map[string]*livekit.IngressInfo, listed []*livekit.IngressInfo) {
	require.Len(t, listed, len(expected))
	seen := make(map[string]struct{}, len(listed))
	for _, got := range listed {
		want, ok := expected[got.IngressId]
		require.True(t, ok, "ingress %s was never stored", got.IngressId)
		_, dup := seen[got.IngressId]
		require.False(t, dup, "ingress %s listed twice", got.IngressId)
		seen[got.IngressId] = struct{}{}
		require.True(t, proto.Equal(want, got), "ingress %s came back as %v, stored %v", got.IngressId, got, want)
	}
}

func TestListIngressWithoutRoomScansTheHash(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)
	ctx := context.Background()

	// more ingresses than two HSCAN chunks
	expected := seedIngresses(t, rc, 1207)

	// every stored ingress comes back, once, exactly as stored, with its state when it has one
	all, err := rs.ListIngress(ctx, "")
	require.NoError(t, err)
	requireSameIngresses(t, expected, all)

	// the listing by room takes the other path and must agree with it
	inRoom := make(map[string]*livekit.IngressInfo)
	for id, info := range expected {
		if info.RoomName == "room-3" {
			inRoom[id] = info
		}
	}
	require.NotEmpty(t, inRoom)
	byRoom, err := rs.ListIngress(ctx, "room-3")
	require.NoError(t, err)
	requireSameIngresses(t, inRoom, byRoom)
}

func TestListIngressWithoutRoomOnEmptyHash(t *testing.T) {
	rc := sharedRedisClients(t, 1)[0]
	rs := service.NewRedisStore(rc)

	infos, err := rs.ListIngress(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, infos)
}
