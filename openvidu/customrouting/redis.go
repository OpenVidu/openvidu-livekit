// Copyright 2024 OpenVidu
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

package customrouting

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const NodesRoomOpenViduKey = "node_rooms_openvidu:"

func RegisterRoomInNode(ctx context.Context, rc redis.UniversalClient, roomName string, nodeID string) {
	rc.SAdd(ctx, NodesRoomOpenViduKey+nodeID, roomName)
}

func UnregisterRoomFromNode(ctx context.Context, rc redis.UniversalClient, roomName string, nodeID string) {
	rc.SRem(context.Background(), NodesRoomOpenViduKey+nodeID, roomName)
}

func ListRoomsForNode(ctx context.Context, rc redis.UniversalClient, nodeID string) ([]string, error) {
	rooms, err := rc.SMembers(ctx, NodesRoomOpenViduKey+nodeID).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list rooms for node: %w", err)
	}
	return rooms, nil
}
