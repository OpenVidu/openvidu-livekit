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
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc/pkg/bus/redisbus"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/livekit-server/pkg/telemetry/telemetryfakes"
)

func TestIngressStateNamesItsRoom(t *testing.T) {
	ctx := context.Background()
	r := redisClientDocker(t)
	rs := service.NewRedisStore(r)
	telemetry := &telemetryfakes.FakeTelemetryService{}
	io, err := service.NewIOInfoService(redisbus.New(r), rs, rs, rs, telemetry)
	require.NoError(t, err)
	require.NoError(t, rs.StoreRoom(ctx, &livekit.Room{Sid: "RM_x", Name: "qa-ingress"}, &livekit.RoomInternal{}))

	publish := func(t *testing.T, ingressID string, roomName string, roomID string) *livekit.IngressInfo {
		require.NoError(t, rs.StoreIngress(ctx, &livekit.IngressInfo{
			IngressId: ingressID, StreamKey: "key-" + ingressID, RoomName: roomName,
			State: &livekit.IngressState{Status: livekit.IngressState_ENDPOINT_INACTIVE},
		}))
		calls := telemetry.IngressStartedCallCount()
		_, err := io.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{IngressId: ingressID, State: &livekit.IngressState{
			Status: livekit.IngressState_ENDPOINT_PUBLISHING, ResourceId: "RT_" + ingressID, RoomId: roomID,
		}})
		require.NoError(t, err)
		require.Equal(t, calls+1, telemetry.IngressStartedCallCount())
		_, started := telemetry.IngressStartedArgsForCall(calls)
		stored, err := rs.LoadIngress(ctx, ingressID)
		require.NoError(t, err)
		require.Equal(t, started.State.RoomId, stored.State.RoomId)
		return started
	}

	t.Run("a publishing state without room_id names the active room", func(t *testing.T) {
		require.Equal(t, "RM_x", publish(t, "IN_1", "qa-ingress", "").State.RoomId)
	})

	t.Run("a room that is not active leaves room_id empty", func(t *testing.T) {
		require.Empty(t, publish(t, "IN_2", "missing-room", "").State.RoomId)
	})

	t.Run("a state that names its room keeps it", func(t *testing.T) {
		require.Equal(t, "RM_y", publish(t, "IN_3", "qa-ingress", "RM_y").State.RoomId)
	})
}
