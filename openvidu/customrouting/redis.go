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
