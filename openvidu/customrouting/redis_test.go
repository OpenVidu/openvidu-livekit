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
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return mr, rc
}

func seedNode(t *testing.T, mr *miniredis.Miniredis, nodeID, nodeIP, relayAddr string) {
	t.Helper()
	b, err := json.Marshal(NodeOpenVidu{NodeId: nodeID, NodeIp: nodeIP, RelayAddress: relayAddr})
	require.NoError(t, err)
	mr.HSet(NodesOpenViduKey, nodeID, string(b))
}

func readNodes(t *testing.T, rc redis.UniversalClient) map[string]NodeOpenVidu {
	t.Helper()
	raw, err := rc.HGetAll(context.Background(), NodesOpenViduKey).Result()
	require.NoError(t, err)
	out := make(map[string]NodeOpenVidu, len(raw))
	for id, v := range raw {
		var n NodeOpenVidu
		require.NoError(t, json.Unmarshal([]byte(v), &n))
		out[id] = n
	}
	return out
}

// A fresh node with no stale entries is simply registered.
func TestRegisterNode_AddsWhenNoStale(t *testing.T) {
	_, rc := newTestRedis(t)

	require.NoError(t, registerNode(context.Background(), rc, "node-1", "10.0.0.1", "10.0.0.1"))

	nodes := readNodes(t, rc)
	require.Len(t, nodes, 1)
	require.Equal(t, NodeOpenVidu{NodeId: "node-1", NodeIp: "10.0.0.1", RelayAddress: "10.0.0.1"}, nodes["node-1"])
}

// A stale entry advertising the same IP is replaced (HDEL+HSET) atomically; the
// end state has exactly the new node and never both/neither.
func TestRegisterNode_ReplacesStaleSameIP(t *testing.T) {
	mr, rc := newTestRedis(t)
	seedNode(t, mr, "node-old", "10.0.0.1", "10.0.0.1")

	require.NoError(t, registerNode(context.Background(), rc, "node-new", "10.0.0.1", "10.0.0.1"))

	nodes := readNodes(t, rc)
	require.Len(t, nodes, 1, "stale same-IP entry must be removed")
	require.Contains(t, nodes, "node-new")
	require.NotContains(t, nodes, "node-old")
	require.Equal(t, "10.0.0.1", nodes["node-new"].NodeIp)
}

// Nodes with a different IP are left untouched — only same-IP staleness is
// reconciled.
func TestRegisterNode_KeepsDifferentIPNodes(t *testing.T) {
	mr, rc := newTestRedis(t)
	seedNode(t, mr, "node-a", "10.0.0.9", "10.0.0.9")

	require.NoError(t, registerNode(context.Background(), rc, "node-b", "10.0.0.1", "10.0.0.1"))

	nodes := readNodes(t, rc)
	require.Len(t, nodes, 2)
	require.Contains(t, nodes, "node-a")
	require.Contains(t, nodes, "node-b")
}

// If the node id is already registered, registration is a no-op and the stored
// entry is not overwritten (HEXISTS short-circuit).
func TestRegisterNode_NoopWhenAlreadyRegistered(t *testing.T) {
	mr, rc := newTestRedis(t)
	seedNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	require.NoError(t, registerNode(context.Background(), rc, "node-1", "10.0.0.99", "10.0.0.99"))

	nodes := readNodes(t, rc)
	require.Len(t, nodes, 1)
	require.Equal(t, "10.0.0.1", nodes["node-1"].NodeIp, "existing entry must not be overwritten")
}

// Registering replaces every stale entry that shares the IP, not just one.
func TestRegisterNode_ReplacesMultipleStaleSameIP(t *testing.T) {
	mr, rc := newTestRedis(t)
	seedNode(t, mr, "node-old-1", "10.0.0.1", "10.0.0.1")
	seedNode(t, mr, "node-old-2", "10.0.0.1", "10.0.0.1")
	seedNode(t, mr, "keep", "10.0.0.2", "10.0.0.2")

	require.NoError(t, registerNode(context.Background(), rc, "node-new", "10.0.0.1", "10.0.0.1"))

	nodes := readNodes(t, rc)
	require.Len(t, nodes, 2)
	require.Contains(t, nodes, "node-new")
	require.Contains(t, nodes, "keep")
	require.NotContains(t, nodes, "node-old-1")
	require.NotContains(t, nodes, "node-old-2")
}

// A malformed sibling entry must not block a healthy node from registering —
// otherwise that node stays absent from nodes_openvidu and gets spurious TURN
// denials. The corrupt entry is skipped; the registration still succeeds.
func TestRegisterNode_SkipsMalformedSibling(t *testing.T) {
	mr, rc := newTestRedis(t)
	mr.HSet(NodesOpenViduKey, "corrupt", "{ not valid json")
	seedNode(t, mr, "node-old", "10.0.0.1", "10.0.0.1")

	require.NoError(t, registerNode(context.Background(), rc, "node-new", "10.0.0.1", "10.0.0.1"))

	raw, err := rc.HGetAll(context.Background(), NodesOpenViduKey).Result()
	require.NoError(t, err)
	require.Contains(t, raw, "node-new", "healthy node must register despite a corrupt sibling")
	require.NotContains(t, raw, "node-old", "stale same-IP entry is still replaced")
}
