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
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/livekit/livekit-server/pkg/config"
)

// NodeOpenVidu represents a node registration message.
type NodeOpenVidu struct {
	NodeId       string `json:"nodeId"`
	NodeIP       string `json:"nodeIP"`
	RelayAddress string `json:"relayAddress"`
}

const NodesRoomOpenViduKey = "node_rooms_openvidu:"
const NodesOpenViduKey = "nodes_openvidu"

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

func RegisterNodeCustom(ctx context.Context, rc redis.UniversalClient, nodeId string) error {
	globalConfig := config.GetGlobalConfig()
	if globalConfig == nil {
		return nil
	}

	nodeIP := globalConfig.RTC.NodeIP
	if nodeIP == "" {
		return nil
	}

	exist, err := rc.HExists(ctx, NodesOpenViduKey, nodeId).Result()
	if err != nil {
		return fmt.Errorf("failed to check if node is registered: %w", err)
	}
	if exist {
		return nil
	}

	rawNodes, err := rc.HGetAll(ctx, NodesOpenViduKey).Result()
	if err != nil {
		return fmt.Errorf("failed to get all nodes: %w", err)
	}

	// collect IDs of old nodes to remove them
	var nodeIDsToRemove []string
	for _, rawNode := range rawNodes {
		var ovNode NodeOpenVidu
		err := json.Unmarshal([]byte(rawNode), &ovNode)
		if err != nil {
			return fmt.Errorf("failed to unmarshal node: %w", err)
		}
		if ovNode.NodeIP == nodeIP && ovNode.NodeId != nodeId {
			nodeIDsToRemove = append(nodeIDsToRemove, ovNode.NodeId)
		}
	}

	// remove old nodes if any exist
	if len(nodeIDsToRemove) > 0 {
		if err := rc.HDel(ctx, NodesOpenViduKey, nodeIDsToRemove...).Err(); err != nil {
			return fmt.Errorf("failed to delete nodes: %w", err)
		}
	}

	ovNode := NodeOpenVidu{
		NodeId:       nodeId,
		NodeIP:       nodeIP,
		RelayAddress: globalConfig.ResolvedRelayAddress,
	}

	jsonOvNode, err := json.Marshal(ovNode)
	if err != nil {
		return fmt.Errorf("failed to marshal node: %w", err)
	}

	if err := rc.HSet(ctx, NodesOpenViduKey, nodeId, jsonOvNode).Err(); err != nil {
		return fmt.Errorf("failed to set node: %w", err)
	}

	return nil
}

func UnregisterNodeCustom(ctx context.Context, rc redis.UniversalClient, nodeID string) error {
	if err := rc.HDel(ctx, NodesOpenViduKey, nodeID).Err(); err != nil {
		return fmt.Errorf("failed to unregister node: %w", err)
	}
	return nil
}
