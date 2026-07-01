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

	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-server/pkg/config"
)

// NodeOpenVidu represents a node registration message.
type NodeOpenVidu struct {
	NodeId       string `json:"nodeId"`
	NodeIp       string `json:"nodeIp"`
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

	nodeIP := globalConfig.RTC.NodeIP.PrimaryIP()
	if nodeIP == "" {
		return nil
	}

	return registerNode(ctx, rc, nodeId, nodeIP, globalConfig.ResolvedRelayAddress)
}

// registerNode registers nodeId in the nodes_openvidu hash, replacing any stale
// entries that advertise the same nodeIP.
//
// The removal of stale entries (HDEL) and the insertion of the new entry (HSET)
// are performed together in a single MULTI/EXEC transaction. This guarantees a
// concurrent reader (notably the TURN permission check, which does an HGETALL of
// this hash) can never observe a transient window where the IP is absent from
// the hash. Done as two separate commands, recycling a node's IP would briefly
// drop it from the allow set and cause spurious "peer IP not in cluster nodes"
// TURN denials for that IP.
func registerNode(ctx context.Context, rc redis.UniversalClient, nodeId, nodeIP, relayAddress string) error {
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

	// collect IDs of stale nodes advertising the same IP so they can be replaced
	// atomically together with this node's registration
	var nodeIDsToRemove []string
	for _, rawNode := range rawNodes {
		var ovNode NodeOpenVidu
		if err := json.Unmarshal([]byte(rawNode), &ovNode); err != nil {
			// A single malformed sibling entry must not block this node from
			// registering — that would leave it absent from nodes_openvidu and
			// cause spurious TURN denials. Skip it, mirroring the reader's
			// tolerance (fetchAllowedIPs in the TURN permission check).
			logger.Warnw("skipping malformed node entry during registration", err, "raw", rawNode)
			continue
		}
		if ovNode.NodeIp == nodeIP && ovNode.NodeId != nodeId {
			nodeIDsToRemove = append(nodeIDsToRemove, ovNode.NodeId)
		}
	}

	ovNode := NodeOpenVidu{
		NodeId:       nodeId,
		NodeIp:       nodeIP,
		RelayAddress: relayAddress,
	}

	jsonOvNode, err := json.Marshal(ovNode)
	if err != nil {
		return fmt.Errorf("failed to marshal node: %w", err)
	}

	// Atomic replace: HDEL stale entries + HSET this node in one transaction so
	// the IP is never transiently missing from nodes_openvidu.
	pipe := rc.TxPipeline()
	if len(nodeIDsToRemove) > 0 {
		pipe.HDel(ctx, NodesOpenViduKey, nodeIDsToRemove...)
	}
	pipe.HSet(ctx, NodesOpenViduKey, nodeId, jsonOvNode)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to register node: %w", err)
	}

	return nil
}

func UnregisterNodeCustom(ctx context.Context, rc redis.UniversalClient, nodeID string) error {
	if err := rc.HDel(ctx, NodesOpenViduKey, nodeID).Err(); err != nil {
		return fmt.Errorf("failed to unregister node: %w", err)
	}
	return nil
}
