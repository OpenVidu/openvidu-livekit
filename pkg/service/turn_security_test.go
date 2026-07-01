// BEGIN OPENVIDU BLOCK
package service

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pion/turn/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"

	"github.com/livekit/livekit-server/openvidu/customrouting"
	"github.com/livekit/livekit-server/pkg/config"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newMiniredis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return mr, rc
}

func setNode(t *testing.T, mr *miniredis.Miniredis, nodeID, nodeIP, relayAddr string) {
	t.Helper()
	node := customrouting.NodeOpenVidu{
		NodeId:       nodeID,
		NodeIp:       nodeIP,
		RelayAddress: relayAddr,
	}
	b, err := json.Marshal(node)
	require.NoError(t, err)
	mr.HSet(customrouting.NodesOpenViduKey, nodeID, string(b))
}

func checkPermission(handler func(net.Addr, net.IP) bool, ip string) bool {
	return handler(nil, net.ParseIP(ip))
}

// ---------------------------------------------------------------------------
// Static mode (no Redis)
// ---------------------------------------------------------------------------

func TestTURNSecurity_Static_AllowsNodeIP(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
}

func TestTURNSecurity_Static_AllowsRelayAddress(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "192.168.1.1"))
}

func TestTURNSecurity_Static_DeniesUnknownIP(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.False(t, checkPermission(handler, "172.16.0.99"))
}

func TestTURNSecurity_Static_EmptyConfig(t *testing.T) {
	conf := &config.Config{}

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.False(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "127.0.0.1"))
}

func TestTURNSecurity_Static_SameNodeIPAndRelay(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "10.0.0.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Static_IPv6(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V6: "::1"}
	conf.ResolvedRelayAddress = "fd00::1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "::1"))
	require.True(t, checkPermission(handler, "fd00::1"))
	require.False(t, checkPermission(handler, "fd00::2"))
}

func TestTURNSecurity_Static_IPv4MappedIPv6(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	// Go normalizes ::ffff:10.0.0.1 to "10.0.0.1".
	mapped := net.ParseIP("::ffff:10.0.0.1")
	require.True(t, handler(nil, mapped))
}

// ---------------------------------------------------------------------------
// Redis mode — allow / deny
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_AllowsRegisteredNodeIP(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
}

func TestTURNSecurity_Redis_AllowsRegisteredRelayAddress(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "192.168.1.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "192.168.1.1"))
}

func TestTURNSecurity_Redis_DeniesUnknownIP(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.False(t, checkPermission(handler, "172.16.0.99"))
}

func TestTURNSecurity_Redis_MultipleNodes(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")
	setNode(t, mr, "node-3", "10.0.0.3", "192.168.1.3")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.2"))
	require.True(t, checkPermission(handler, "10.0.0.3"))
	require.True(t, checkPermission(handler, "192.168.1.3"))
	require.False(t, checkPermission(handler, "10.0.0.4"))
}

func TestTURNSecurity_Redis_EmptyHash(t *testing.T) {
	_, rc := newMiniredis(t)

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.False(t, checkPermission(handler, "10.0.0.1"))
}

func TestTURNSecurity_Redis_DifferentNodeIPAndRelay(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "192.168.1.100")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "192.168.1.100"))
	require.False(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Redis_IPv4MappedIPv6(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	mapped := net.ParseIP("::ffff:10.0.0.1")
	require.True(t, handler(nil, mapped))
}

// ---------------------------------------------------------------------------
// Local IPs — always allowed (both modes)
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_AllowsLocalMachineIPs(t *testing.T) {
	// Even without the local IPs being registered in Redis, the permission
	// handler should allow them because the TURN relay is embedded and may
	// forward to any local interface (e.g. Docker bridge).
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
	if err != nil {
		t.Skipf("could not get local IP addresses: %v", err)
	}
	for _, ip := range localIPs {
		require.True(t, checkPermission(handler, ip), "local IP %s should be allowed", ip)
	}

	// Remote node IP should still be allowed via Redis.
	require.True(t, checkPermission(handler, "10.0.0.1"))
	// Unknown remote IP should be denied.
	require.False(t, checkPermission(handler, "99.99.99.99"))
}

func TestTURNSecurity_Static_AllowsLocalMachineIPs(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
	if err != nil {
		t.Skipf("could not get local IP addresses: %v", err)
	}
	for _, ip := range localIPs {
		require.True(t, checkPermission(handler, ip), "local IP %s should be allowed", ip)
	}

	// Configured IPs should still be allowed too.
	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "192.168.1.1"))
}

func TestTURNSecurity_LocalIPs_NotExposedToRemoteNodes(t *testing.T) {
	// Verify that local IPs are a property of the TURN server instance,
	// NOT stored in Redis. A remote node should not be able to claim
	// arbitrary local IPs that other nodes' TURN servers would then allow.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "remote-node", "10.0.0.99", "10.0.0.99")

	s := NewTURNSecurity(&config.Config{}, rc)

	// The allowed set from Redis should only contain the registered IPs.
	allowed, err := s.fetchAllowedIPs()
	require.NoError(t, err)
	require.Contains(t, allowed, "10.0.0.99")
	// Docker bridge IPs of the remote node should NOT be in the Redis set.
	require.NotContains(t, allowed, "172.17.0.1")
}

// ---------------------------------------------------------------------------
// Redis mode — cache behavior
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_NewNodeImmediatelyAllowed(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.False(t, checkPermission(handler, "10.0.0.2"))

	// Add a new node — immediately visible on next check.
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")
	require.True(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Redis_RemovedNodeCachedUntilRefresh(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Remove node — still allowed because of cache.
	mr.HDel(customrouting.NodesOpenViduKey, "node-1")
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Expire cache — removal now takes effect on next check.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	require.False(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Redis_NodeIPChangeImmediatelyReflected(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Node changes IP.
	setNode(t, mr, "node-1", "10.0.0.99", "10.0.0.99")
	require.True(t, checkPermission(handler, "10.0.0.99"))
	require.False(t, checkPermission(handler, "10.0.0.1"))
}

// ---------------------------------------------------------------------------
// Redis mode — error handling
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_CachedIPsSurviveRedisError(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Redis error — cached IP still served from cache.
	mr.SetError("LOADING Redis is loading the dataset in memory")
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Uncached IP denied (cannot refresh from Redis).
	require.False(t, checkPermission(handler, "10.0.0.2"))

	// Clear error — new IPs can be resolved again.
	mr.SetError("")
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")
	require.True(t, checkPermission(handler, "10.0.0.2"))
	require.True(t, checkPermission(handler, "10.0.0.1"))
}

// ---------------------------------------------------------------------------
// Redis mode — malformed data
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_MalformedJSONSkipped(t *testing.T) {
	mr, rc := newMiniredis(t)

	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")
	mr.HSet(customrouting.NodesOpenViduKey, "node-bad", "not-valid-json")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "172.16.0.99"))
}

func TestTURNSecurity_Redis_NodeWithEmptyIPs(t *testing.T) {
	mr, rc := newMiniredis(t)

	setNode(t, mr, "node-empty", "", "")
	setNode(t, mr, "node-1", "10.0.0.1", "")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "0.0.0.0"))
}

// ---------------------------------------------------------------------------
// Redis mode — overlapping IPs across nodes
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_OverlappingIPsAcrossNodes(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "192.168.1.1")
	setNode(t, mr, "node-2", "10.0.0.1", "192.168.1.2")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "192.168.1.1"))
	require.True(t, checkPermission(handler, "192.168.1.2"))

	// Remove one node — all IPs still cached.
	mr.HDel(customrouting.NodesOpenViduKey, "node-1")
	require.True(t, checkPermission(handler, "192.168.1.1")) // still in cache

	// Expire cache — removal now takes effect.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	// Shared IP still allowed via node-2, node-1's relay denied.
	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "192.168.1.2"))
	require.False(t, checkPermission(handler, "192.168.1.1"))
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_ConcurrentAccess(t *testing.T) {
	mr, rc := newMiniredis(t)
	for i := 0; i < 100; i++ {
		setNode(t, mr, fmt.Sprintf("node-%d", i), fmt.Sprintf("10.0.0.%d", i+1), fmt.Sprintf("10.0.0.%d", i+1))
	}

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				idx := (g+j)%10 + 1
				require.True(t, checkPermission(handler, fmt.Sprintf("10.0.0.%d", idx)))
				require.False(t, checkPermission(handler, "99.99.99.99"))
			}
		}(g)
	}
	wg.Wait()
}

func TestTURNSecurity_Static_ConcurrentAccess(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				require.True(t, checkPermission(handler, "10.0.0.1"))
				require.False(t, checkPermission(handler, "99.99.99.99"))
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Redis mode — large cluster
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_LargeCluster(t *testing.T) {
	mr, rc := newMiniredis(t)

	const numNodes = 200
	for i := 0; i < numNodes; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)
		relay := fmt.Sprintf("192.168.%d.%d", (i/256)%256, i%256)
		setNode(t, mr, fmt.Sprintf("node-%d", i), ip, relay)
	}

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Spot-check first, last, and middle nodes.
	require.True(t, checkPermission(handler, "10.0.0.0"))
	require.True(t, checkPermission(handler, "192.168.0.0"))
	require.True(t, checkPermission(handler, fmt.Sprintf("10.%d.%d.%d", (numNodes-1)/65536, ((numNodes-1)/256)%256, (numNodes-1)%256)))
	require.True(t, checkPermission(handler, fmt.Sprintf("192.168.%d.%d", ((numNodes/2)/256)%256, (numNodes/2)%256)))
	require.False(t, checkPermission(handler, "172.31.255.255"))
}

func TestTURNSecurity_Redis_LargeClusterNodeAddedAndRemoved(t *testing.T) {
	mr, rc := newMiniredis(t)

	const numNodes = 100
	for i := 0; i < numNodes; i++ {
		setNode(t, mr, fmt.Sprintf("node-%d", i), fmt.Sprintf("10.0.%d.%d", (i/256)%256, i%256), fmt.Sprintf("10.0.%d.%d", (i/256)%256, i%256))
	}

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// All original nodes allowed.
	require.True(t, checkPermission(handler, "10.0.0.0"))
	require.True(t, checkPermission(handler, "10.0.0.50"))

	// Add a new node — immediately visible (cache miss triggers refresh).
	setNode(t, mr, "node-new", "10.99.99.99", "10.99.99.99")
	require.True(t, checkPermission(handler, "10.99.99.99"))

	// Remove a node — still cached.
	mr.HDel(customrouting.NodesOpenViduKey, "node-0")
	require.True(t, checkPermission(handler, "10.0.0.0")) // still in cache

	// Expire cache — removal now takes effect.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	require.False(t, checkPermission(handler, "10.0.0.0"))
	require.True(t, checkPermission(handler, "10.0.0.50"))
}

// ---------------------------------------------------------------------------
// Redis mode — cache edge cases
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_ExpiredCacheServedStaleDuringRedisOutage(t *testing.T) {
	// After the cache TTL expires, if Redis is unavailable the stale cache
	// is served to keep TURN working. Unknown IPs are still denied.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Expire cache and break Redis.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()
	mr.SetError("LOADING Redis is loading the dataset in memory")

	// Stale cache keeps TURN working for known IPs.
	require.True(t, checkPermission(handler, "10.0.0.1"))
	// Unknown IPs are still denied.
	require.False(t, checkPermission(handler, "10.0.0.2"))

	// Recovery: fix Redis → fresh data again.
	mr.SetError("")
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")

	// Expire cache to force refresh.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Redis_MissWithRedisErrorPreservesCache(t *testing.T) {
	// A failed Redis fetch (triggered by a miss for an unknown IP) must
	// not corrupt the existing fresh cache — other cached IPs keep working.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Redis error — miss for unknown IP serves stale (IP not found), cache intact.
	mr.SetError("LOADING Redis is loading the dataset in memory")
	require.False(t, checkPermission(handler, "10.0.0.2")) // miss → stale served → not in stale → denied
	require.True(t, checkPermission(handler, "10.0.0.1"))  // still served from cache

	mr.SetError("")
}

func TestTURNSecurity_Redis_TTLExpiryRefreshesKnownIP(t *testing.T) {
	// After the TTL expires, even a previously-allowed IP triggers a full
	// cache refresh — so changes in Redis (like an IP swap) become visible.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Change the node's IP in Redis while cache is still fresh.
	setNode(t, mr, "node-1", "10.0.0.99", "10.0.0.99")

	// Still cached — old IP allowed, new IP triggers refresh.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Expire cache.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	// After expiry the old IP triggers a refresh and is no longer found.
	require.False(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.99"))
}

func TestTURNSecurity_Redis_SequentialMissesRefreshCache(t *testing.T) {
	// Each cache miss triggers a fresh Redis fetch, so nodes added between
	// sequential misses are picked up immediately.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache with node-1 only.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// node-2 not in Redis yet — miss + refresh → not found.
	require.False(t, checkPermission(handler, "10.0.0.2"))

	// Add nodes between misses.
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")
	setNode(t, mr, "node-3", "10.0.0.3", "10.0.0.3")

	// Miss for node-2 triggers a new refresh → finds both new nodes.
	require.True(t, checkPermission(handler, "10.0.0.2"))
	// node-3 now in cache from the same refresh — cache hit.
	require.True(t, checkPermission(handler, "10.0.0.3"))
}

func TestTURNSecurity_Redis_ConcurrentMissAllGetCorrectResult(t *testing.T) {
	// Many goroutines miss on the same new IP simultaneously. The dedup
	// logic must ensure every goroutine gets the correct (allowed) result,
	// regardless of which one actually performs the Redis fetch.
	mr, rc := newMiniredis(t)
	for i := 0; i < 5; i++ {
		setNode(t, mr, fmt.Sprintf("node-%d", i), fmt.Sprintf("10.0.0.%d", i+1), fmt.Sprintf("10.0.0.%d", i+1))
	}

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Add a new node after cache is populated.
	setNode(t, mr, "node-new", "10.0.0.99", "10.0.0.99")

	// Many goroutines all miss on the new IP simultaneously.
	const numGoroutines = 50
	var wg sync.WaitGroup
	results := make([]bool, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = checkPermission(handler, "10.0.0.99")
		}(i)
	}
	wg.Wait()

	// All must see the new node.
	for i, r := range results {
		require.True(t, r, "goroutine %d should have seen the new node", i)
	}
}

func TestTURNSecurity_Redis_ConcurrentMissDeniedIPStaysDenied(t *testing.T) {
	// Mirror of the above: concurrent misses for a genuinely unknown IP
	// must all return false, even though each miss triggers a refresh.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))

	const numGoroutines = 50
	var wg sync.WaitGroup
	results := make([]bool, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = checkPermission(handler, "99.99.99.99")
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		require.False(t, r, "goroutine %d should have denied unknown IP", i)
	}
}

func TestTURNSecurity_Redis_AllNodesRemovedAfterTTL(t *testing.T) {
	// If every node is removed from Redis and the cache expires,
	// all IPs must be denied.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")
	setNode(t, mr, "node-2", "10.0.0.2", "10.0.0.2")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.2"))

	// Remove all nodes.
	mr.HDel(customrouting.NodesOpenViduKey, "node-1")
	mr.HDel(customrouting.NodesOpenViduKey, "node-2")

	// Still cached.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Expire cache.
	s.cacheMu.Lock()
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()

	// Everything denied.
	require.False(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Redis_ColdStartWithRedisDown(t *testing.T) {
	// If Redis is unavailable from the very start (no cache populated),
	// all IPs must be denied — there is no stale data to fall back on.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	handler := s.PermissionHandler()

	// Break Redis before any cache is populated.
	mr.SetError("LOADING Redis is loading the dataset in memory")

	require.False(t, checkPermission(handler, "10.0.0.1"))

	// Recovery: Redis comes back → cache populated → allowed.
	mr.SetError("")
	require.True(t, checkPermission(handler, "10.0.0.1"))
}

func TestTURNSecurity_DefaultCacheTTL(t *testing.T) {
	require.Equal(t, time.Minute, defaultTURNCacheTTL)

	s := NewTURNSecurity(&config.Config{}, nil)
	require.Equal(t, time.Minute, s.cacheTTL)
}

func TestTURNSecurity_Redis_RealTTLExpiry(t *testing.T) {
	// Verify that the cacheTTL duration actually controls cache expiry
	// by using a very short TTL and letting it elapse naturally.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)
	s.cacheTTL = 10 * time.Millisecond
	handler := s.PermissionHandler()

	// Populate cache.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Change the node's IP in Redis while cache is still fresh.
	setNode(t, mr, "node-1", "10.0.0.99", "10.0.0.99")

	// Cache is fresh — old IP still allowed, new IP triggers a miss+refresh.
	require.True(t, checkPermission(handler, "10.0.0.1"))

	// Wait for the TTL to expire naturally.
	time.Sleep(20 * time.Millisecond)

	// After real TTL expiry the old IP triggers a refresh and is gone.
	require.False(t, checkPermission(handler, "10.0.0.1"))
	require.True(t, checkPermission(handler, "10.0.0.99"))
}

// ---------------------------------------------------------------------------
// PermissionHandler method
// ---------------------------------------------------------------------------

func TestTURNSecurity_PermissionHandlerReturnsNonNil(t *testing.T) {
	s := NewTURNSecurity(&config.Config{}, nil)
	require.NotNil(t, s.PermissionHandler())
}

// ---------------------------------------------------------------------------
// OpenVidu relay PacketConn (port restriction + telemetry)
// ---------------------------------------------------------------------------

// mockPacketConn is a minimal net.PacketConn for testing.
type mockPacketConn struct {
	net.PacketConn
	writtenBytes int
	writtenAddr  net.Addr
	closed       bool
}

func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	m.writtenBytes = len(p)
	m.writtenAddr = addr
	return len(p), nil
}

func (m *mockPacketConn) Close() error                             { m.closed = true; return nil }
func (m *mockPacketConn) LocalAddr() net.Addr                      { return &net.UDPAddr{IP: net.IPv4zero, Port: 0} }
func (m *mockPacketConn) ReadFrom(p []byte) (int, net.Addr, error) { return 0, nil, nil }

func TestTURNSecurity_PortRestriction_WithinRange(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	n, err := conn.WriteTo([]byte("hello"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500})
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, 5, inner.writtenBytes)
}

func TestTURNSecurity_PortRestriction_BelowRange(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	_, err := conn.WriteTo([]byte("hello"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22})
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")
	require.Equal(t, 0, inner.writtenBytes)
}

func TestTURNSecurity_PortRestriction_AboveRange(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	_, err := conn.WriteTo([]byte("hello"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 6379})
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")
	require.Equal(t, 0, inner.writtenBytes)
}

func TestTURNSecurity_PortRestriction_BoundaryMin(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	n, err := conn.WriteTo([]byte("ok"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5000})
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestTURNSecurity_PortRestriction_BoundaryMax(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	n, err := conn.WriteTo([]byte("ok"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 6000})
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestTURNSecurity_PortRestriction_BoundaryMinMinusOne(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	_, err := conn.WriteTo([]byte("no"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 4999})
	require.Error(t, err)
}

func TestTURNSecurity_PortRestriction_BoundaryMaxPlusOne(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	_, err := conn.WriteTo([]byte("no"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 6001})
	require.Error(t, err)
}

func TestTURNSecurity_PortRestriction_Passthrough(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	// LocalAddr should delegate without port filtering.
	require.NotNil(t, conn.LocalAddr())
	require.NoError(t, conn.Close())

	_, _, err := conn.ReadFrom(make([]byte, 10))
	require.NoError(t, err)
}

func TestTURNSecurity_PortRestriction_TCPAddr(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 5000, maxPort: 6000}

	// TCP addr within range should be allowed.
	n, err := conn.WriteTo([]byte("tcp"), &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500})
	require.NoError(t, err)
	require.Equal(t, 3, n)

	// TCP addr outside range should be denied.
	_, err = conn.WriteTo([]byte("tcp"), &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80})
	require.Error(t, err)
}

func TestTURNSecurity_PortRestriction_AllDeniedWhenZero(t *testing.T) {
	// When both minPort and maxPort are 0 (no ICE port range configured),
	// all peer relay ports are denied.
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, minPort: 0, maxPort: 0}

	_, err := conn.WriteTo([]byte("any"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22})
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")

	_, err = conn.WriteTo([]byte("any"), &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 65535})
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")
}

func TestTURNSecurity_OpenViduRelayAddrGen_Wraps(t *testing.T) {
	gen := newOpenViduRelayAddrGen(nil, 5000, 6000, false, true)
	require.NotNil(t, gen)
	require.Equal(t, uint16(5000), gen.minPort)
	require.Equal(t, uint16(6000), gen.maxPort)
	require.False(t, gen.standalone)
	require.True(t, gen.enableRFC6062)
}

func TestTURNSecurity_OpenViduRelayAddrGen_Standalone(t *testing.T) {
	gen := newOpenViduRelayAddrGen(nil, 0, 0, true, false)
	require.NotNil(t, gen)
	require.True(t, gen.standalone)
	require.False(t, gen.enableRFC6062)
}

func TestTURNSecurity_Standalone_CloseDelegate(t *testing.T) {
	// Verify Close delegates to inner even in standalone mode.
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, standalone: true}
	require.NoError(t, conn.Close())
	require.True(t, inner.closed)
}

func TestTURNSecurity_NonStandalone_CloseDelegate(t *testing.T) {
	inner := &mockPacketConn{}
	conn := &openviduRelayPacketConn{PacketConn: inner, standalone: false}
	require.NoError(t, conn.Close())
	require.True(t, inner.closed)
}

func TestTURNSecurity_AllocateConn_PortOutOfRange(t *testing.T) {
	inner := &mockRelayAddrGen{}
	// RFC 6062 enabled so the port restriction is the only thing under test.
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, true)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80},
	})
	require.Nil(t, conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")
}

func TestTURNSecurity_AllocateConn_PortInRange(t *testing.T) {
	inner := &mockRelayAddrGen{}
	// RFC 6062 enabled so an in-range Connect is allowed through.
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, true)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500},
	})
	require.NoError(t, err)
	require.NotNil(t, conn)
	_ = conn.Close()
}

func TestTURNSecurity_AllocateConn_ZeroPortRange_AllDenied(t *testing.T) {
	inner := &mockRelayAddrGen{}
	// RFC 6062 enabled so the zero-range port denial is the only thing under test.
	gen := newOpenViduRelayAddrGen(inner, 0, 0, false, true)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500},
	})
	require.Nil(t, conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside allowed range")
}

// ---------------------------------------------------------------------------
// RFC 6062 (TURN TCP allocations) — disabled by default, opt-in to enable
// ---------------------------------------------------------------------------

func TestTURNSecurity_RFC6062_DisabledRejectsConnect(t *testing.T) {
	// With RFC 6062 disabled, an otherwise-valid in-range Connect is rejected
	// and the inner generator is never invoked.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, false)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500},
	})
	require.Nil(t, conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "RFC 6062")
	require.Contains(t, err.Error(), "disabled")
}

func TestTURNSecurity_RFC6062_EnabledAllowsConnect(t *testing.T) {
	// With RFC 6062 enabled, an in-range Connect is allocated through the
	// inner generator.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, true)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500},
	})
	require.NoError(t, err)
	require.NotNil(t, conn)
	_ = conn.Close()
}

func TestTURNSecurity_RFC6062_DisabledTakesPrecedenceOverPort(t *testing.T) {
	// When disabled, the RFC 6062 rejection happens before the port check, so
	// even an in-range port is denied with the RFC 6062 reason — never the
	// port-range reason.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, false)

	conn, err := gen.AllocateConn(turn.AllocateConnConfig{
		Network:    "tcp4",
		RemoteAddr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5500},
	})
	require.Nil(t, conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "RFC 6062")
	require.NotContains(t, err.Error(), "outside allowed range")
}

func TestTURNSecurity_RFC6062_DisabledRejectsTCPAllocate(t *testing.T) {
	// The inbound RFC 6062 path: a TCP Allocate (REQUESTED-TRANSPORT=TCP) goes
	// through AllocateListener. When disabled, it must be rejected so clients
	// cannot create a TCP relay listener at all.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, false)

	ln, addr, err := gen.AllocateListener(turn.AllocateListenerConfig{Network: "tcp4"})
	require.Nil(t, ln)
	require.Nil(t, addr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "RFC 6062")
	require.Contains(t, err.Error(), "disabled")
}

func TestTURNSecurity_RFC6062_EnabledAllowsTCPAllocate(t *testing.T) {
	// When enabled, the TCP Allocate is delegated to the inner generator and a
	// relay listener is returned.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, true)

	ln, addr, err := gen.AllocateListener(turn.AllocateListenerConfig{Network: "tcp4"})
	require.NoError(t, err)
	require.NotNil(t, ln)
	require.NotNil(t, addr)
	_ = ln.Close()
}

func TestTURNSecurity_RFC6062_DisabledLeavesUDPUnaffected(t *testing.T) {
	// Disabling RFC 6062 must not touch UDP relays: AllocatePacketConn (the UDP
	// allocation path) still delegates to the inner generator.
	inner := &mockRelayAddrGen{}
	gen := newOpenViduRelayAddrGen(inner, 5000, 6000, false, false)

	conn, addr, err := gen.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4"})
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NotNil(t, addr)
	_ = conn.Close()
}

// mockRelayAddrGen is a minimal turn.RelayAddressGenerator for testing.
type mockRelayAddrGen struct{}

func (m *mockRelayAddrGen) Validate() error { return nil }
func (m *mockRelayAddrGen) AllocatePacketConn(turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.LocalAddr(), nil
}
func (m *mockRelayAddrGen) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	return ln, ln.Addr(), nil
}
func (m *mockRelayAddrGen) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	server, client := net.Pipe()
	_ = server.Close()
	return client, nil
}

// ---------------------------------------------------------------------------
// CIDR allow/deny policy (allow_restricted_peer_cidrs / deny_peer_cidrs)
// ---------------------------------------------------------------------------
//
// These exercise TURNSecurity.handlePermissionWithCIDRs:
//   - deny_peer_cidrs denies, with precedence over local AND cluster IPs and
//     over the allow list;
//   - allow_restricted_peer_cidrs GRANTS access to any listed IP (private,
//     public, or cluster); and when non-empty it also denies restricted peer
//     IPs that are not listed (narrowing);
//   - default (empty lists) leaves the decision to the cluster/local allowlist.

// firstPrivateLocalIPv4 returns a private IPv4 address of this machine, or skips
// the test if none is available. These IPs are exactly the ones NewTURNSecurity
// discovers into its localIPs set.
func firstPrivateLocalIPv4(t *testing.T) string {
	t.Helper()
	ips, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
	if err != nil {
		t.Skipf("could not get local IPs: %v", err)
	}
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil && p.IsPrivate() {
			return ip
		}
	}
	t.Skip("no private IPv4 local address available")
	return ""
}

// deny_peer_cidrs must deny a registered cluster-node IP (precedence over the
// cluster allowlist).
func TestTURNSecurity_DenyCIDR_OverridesClusterNode(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.5", "10.0.0.5")
	setNode(t, mr, "node-2", "172.16.0.9", "172.16.0.9")

	conf := &config.Config{}
	conf.TURN.DenyPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.False(t, checkPermission(h, "10.0.0.5"),
		"deny CIDR must override a registered cluster-node IP")
	require.True(t, checkPermission(h, "172.16.0.9"),
		"cluster node outside deny range must remain allowed")
}

// deny_peer_cidrs must take precedence over the local-IP fast path.
func TestTURNSecurity_DenyCIDR_OverridesLocalIP(t *testing.T) {
	v4 := firstPrivateLocalIPv4(t)

	_, rc := newMiniredis(t)
	conf := &config.Config{}
	conf.TURN.DenyPeerCIDRs = []string{"0.0.0.0/0"} // deny every IPv4

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.False(t, checkPermission(h, v4),
		"deny CIDR must override the always-allow local-IP fast path")
}

// deny_peer_cidrs takes precedence over allow_restricted_peer_cidrs.
func TestTURNSecurity_DenyCIDR_TakesPrecedenceOverAllow(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.1.2.3", "10.1.2.3")

	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"}
	conf.TURN.DenyPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.False(t, checkPermission(h, "10.1.2.3"),
		"deny list must take precedence over allow list")
}

// allow_restricted_peer_cidrs grants access to any listed IP — including a
// PUBLIC, non-cluster IP. An unlisted public non-cluster IP is still denied.
func TestTURNSecurity_AllowCIDR_GrantsListedPublicIP(t *testing.T) {
	_, rc := newMiniredis(t) // empty cluster
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"1.1.1.0/24"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "1.1.1.1"),
		"a public IP explicitly listed in the allow CIDRs must be permitted")
	require.False(t, checkPermission(h, "8.8.8.8"),
		"a public IP not listed (and not a cluster node) must be denied")
}

// allow_restricted_peer_cidrs grants access to a listed PRIVATE IP even when it
// is not a registered cluster node.
func TestTURNSecurity_AllowCIDR_GrantsListedPrivateNonClusterIP(t *testing.T) {
	_, rc := newMiniredis(t) // empty cluster — 10.1.2.3 is NOT a registered node
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "10.1.2.3"),
		"a listed private IP must be permitted even if it is not a cluster node")
	require.False(t, checkPermission(h, "192.168.1.9"),
		"a restricted IP outside the allow CIDRs must be denied")
}

// allow_restricted_peer_cidrs: a restricted cluster-node IP inside the allow
// range is permitted.
func TestTURNSecurity_AllowCIDR_AllowsClusterNodeInRange(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.1.2.3", "10.1.2.3")

	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "10.1.2.3"),
		"restricted cluster node inside an allow CIDR must be permitted")
}

// allow_restricted_peer_cidrs: a restricted cluster-node IP OUTSIDE the allow
// range is denied — the allow list narrows access even for registered cluster
// nodes.
func TestTURNSecurity_AllowCIDR_DeniesClusterNodeOutOfRange(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "192.168.1.5", "192.168.1.5")

	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.False(t, checkPermission(h, "192.168.1.5"),
		"restricted cluster node outside the allow CIDRs must be denied")
}

// allow_restricted_peer_cidrs with multiple ranges: membership in any listed
// CIDR is permitted.
func TestTURNSecurity_AllowCIDR_MultipleRanges(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.1.2.3", "10.1.2.3")
	setNode(t, mr, "node-2", "192.168.50.7", "192.168.50.7")
	setNode(t, mr, "node-3", "172.16.0.1", "172.16.0.1") // restricted, NOT in either allow CIDR

	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8", "192.168.0.0/16"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "10.1.2.3"))
	require.True(t, checkPermission(h, "192.168.50.7"))
	require.False(t, checkPermission(h, "172.16.0.1"),
		"cluster node outside all allow CIDRs must be denied")
}

// FOOTGUN: the allow-list restricted-gate runs BEFORE the local-IP fast path, so
// a local (restricted) IP that is not covered by the allow list is denied — even
// though local IPs are normally always allowed. When using an allow list,
// operators must include the local/cluster ranges.
func TestTURNSecurity_AllowCIDR_DeniesUnlistedLocalIP(t *testing.T) {
	localIP := firstPrivateLocalIPv4(t)
	_, rc := newMiniredis(t)
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"203.0.113.0/24"} // public range; excludes the local IP
	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.False(t, checkPermission(h, localIP),
		"local restricted IP not in the allow list is denied (allow gate precedes the local fast path)")
}

// Complement of the footgun: a local IP that IS covered by the allow list is
// permitted.
func TestTURNSecurity_AllowCIDR_AllowsListedLocalIP(t *testing.T) {
	localIP := firstPrivateLocalIPv4(t)
	_, rc := newMiniredis(t)
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{fmt.Sprintf("%s/32", localIP)}
	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, localIP),
		"local IP included in the allow list is permitted")
}

// The restricted-IP gate only narrows RESTRICTED IPs: a PUBLIC cluster-node IP
// that is not in the allow list still passes through to the cluster allowlist.
func TestTURNSecurity_AllowCIDR_DoesNotBlockPublicClusterNode(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-pub", "203.0.113.50", "203.0.113.50") // public cluster-node IP
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"} // does not cover the public node

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "203.0.113.50"),
		"public cluster node not in the allow list is still allowed (gate only narrows restricted IPs)")
	require.False(t, checkPermission(h, "198.51.100.7"),
		"public non-cluster IP not in the allow list is denied")
}

// allow_restricted_peer_cidrs also grants in static (no-Redis) mode.
func TestTURNSecurity_Static_AllowCIDR_Grants(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "10.0.0.1"
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, nil).PermissionHandler() // static mode

	require.True(t, checkPermission(h, "10.0.0.1"), "node IP within allow list permitted")
	require.True(t, checkPermission(h, "10.5.5.5"), "any listed IP permitted (even a non-node) in static mode")
	require.False(t, checkPermission(h, "192.168.1.1"), "restricted IP outside allow list denied in static mode")
}

// deny_peer_cidrs overrides even the configured node/relay IP in static mode.
func TestTURNSecurity_Static_DenyCIDR_OverridesNodeIP(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "10.0.0.1"}
	conf.ResolvedRelayAddress = "10.0.0.1"
	conf.TURN.DenyPeerCIDRs = []string{"10.0.0.0/8"}

	h := NewTURNSecurity(conf, nil).PermissionHandler()

	require.False(t, checkPermission(h, "10.0.0.1"),
		"deny CIDR overrides the configured node/relay IP in static mode")
}

// Default (no allow/deny CIDRs): restricted cluster nodes are allowed; restricted
// and public non-cluster IPs are denied.
func TestTURNSecurity_NoCIDRs_DefaultRestrictedHandling(t *testing.T) {
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.1.2.3", "10.1.2.3")

	h := NewTURNSecurity(&config.Config{}, rc).PermissionHandler()

	require.True(t, checkPermission(h, "10.1.2.3"), "restricted cluster node allowed")
	require.False(t, checkPermission(h, "192.168.9.9"), "restricted non-cluster IP denied")
	require.False(t, checkPermission(h, "8.8.8.8"), "public non-cluster IP denied")
}

// CIDR rules work for IPv6 peers too.
func TestTURNSecurity_AllowCIDR_IPv6Grant(t *testing.T) {
	_, rc := newMiniredis(t)
	conf := &config.Config{}
	conf.TURN.AllowRestrictedPeerCIDRs = []string{"2001:db8::/32"}

	h := NewTURNSecurity(conf, rc).PermissionHandler()

	require.True(t, checkPermission(h, "2001:db8::1"), "listed IPv6 peer permitted")
	require.False(t, checkPermission(h, "2001:dead::1"), "unlisted public IPv6 peer denied")
}

// ---------------------------------------------------------------------------
// Refresh dedup: a newer snapshot is only trusted for a positive result
// ---------------------------------------------------------------------------

func TestTURNSecurity_Redis_RefreshRevalidatesNegativeDedup(t *testing.T) {
	// Simulate a poisoned snapshot: the cache is fresh but missing an IP that is
	// actually present in Redis (as if captured during a transient gap). A
	// concurrent refresh for that IP must NOT trust the newer snapshot; it must
	// refetch from Redis and find the IP.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)

	s.cacheMu.Lock()
	s.cachedIPs = map[string]struct{}{"10.0.0.2": {}} // poisoned: missing 10.0.0.1
	s.cacheTime = time.Now().Add(-time.Second)        // older than negativeRevalidateInterval
	s.cacheMu.Unlock()

	// prevCacheTime in the past → the dedup short-circuit would otherwise fire;
	// the snapshot is older than negativeRevalidateInterval so it is revalidated.
	allowed, err := s.refreshCache(time.Time{}, "10.0.0.1")
	require.NoError(t, err)
	_, ok := allowed["10.0.0.1"]
	require.True(t, ok, "negative dedup must refetch and find the present IP")
}

func TestTURNSecurity_Redis_PositiveDedupSkipsRefetch(t *testing.T) {
	// When the newer snapshot already confirms the IP, the dedup short-circuit
	// is used and no Redis fetch happens — proven by breaking Redis and still
	// getting a positive result without error.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)

	s.cacheMu.Lock()
	s.cachedIPs = map[string]struct{}{"10.0.0.1": {}}
	s.cacheTime = time.Now()
	s.cacheMu.Unlock()

	mr.SetError("LOADING Redis is loading the dataset in memory")

	allowed, err := s.refreshCache(time.Time{}, "10.0.0.1")
	require.NoError(t, err, "positive dedup must not hit Redis")
	_, ok := allowed["10.0.0.1"]
	require.True(t, ok)
}

func TestTURNSecurity_Redis_RefreshNegativeDedupStillDeniesAbsentIP(t *testing.T) {
	// Revalidating a negative dedup must not falsely allow: if the IP is also
	// genuinely absent from Redis, the refetched snapshot is negative and the IP
	// stays denied. (Guards against the fix turning every miss into an allow.)
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)

	s.cacheMu.Lock()
	s.cachedIPs = map[string]struct{}{"10.0.0.1": {}}
	s.cacheTime = time.Now().Add(-time.Second) // older than negativeRevalidateInterval → revalidates
	s.cacheMu.Unlock()

	allowed, err := s.refreshCache(time.Time{}, "9.9.9.9")
	require.NoError(t, err)
	_, ok := allowed["9.9.9.9"]
	require.False(t, ok, "genuinely absent IP must stay denied after revalidation")
	// The revalidating refetch also reconciled the cache with real Redis state.
	_, ok = allowed["10.0.0.1"]
	require.True(t, ok)
}

func TestTURNSecurity_Redis_NegativeDedupRateLimitedWhenFresh(t *testing.T) {
	// A negative result against a very fresh snapshot is reused (not refetched),
	// bounding refetch load under a burst of misses for a genuinely-absent IP.
	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", "10.0.0.1", "10.0.0.1")

	s := NewTURNSecurity(&config.Config{}, rc)

	fresh := time.Now()
	s.cacheMu.Lock()
	s.cachedIPs = map[string]struct{}{"10.0.0.1": {}}
	s.cacheTime = fresh
	s.cacheMu.Unlock()

	allowed, err := s.refreshCache(time.Time{}, "9.9.9.9")
	require.NoError(t, err)
	_, ok := allowed["9.9.9.9"]
	require.False(t, ok)

	// No refetch happened within the interval: the snapshot/time are untouched.
	s.cacheMu.RLock()
	require.Equal(t, fresh, s.cacheTime, "fresh negative snapshot must be reused, not refetched")
	s.cacheMu.RUnlock()
}

// ---------------------------------------------------------------------------
// Denied-peer severity classification
// ---------------------------------------------------------------------------

func TestTURNSecurity_IsPublicIP(t *testing.T) {
	// Public/global addresses (a denied relay peer here is a genuine external
	// target → warning); private/local addresses (a benign in-cluster address
	// transiently missing from nodes_openvidu → info).
	cases := []struct {
		ip   string
		want bool
	}{
		{"3.252.97.62", true},    // public (AWS)
		{"108.131.122.27", true}, // public
		{"2001:db8::1", true},    // global unicast IPv6
		{"10.0.0.1", false},      // private
		{"10.10.14.163", false},  // private (cluster node)
		{"172.16.5.4", false},    // private
		{"192.168.1.1", false},   // private
		{"127.0.0.1", false},     // loopback
		{"169.254.1.1", false},   // link-local
		{"fe80::1", false},       // link-local IPv6
		{"fd00::1", false},       // unique-local IPv6 (private)
	}
	for _, c := range cases {
		require.Equal(t, c.want, isPublicIP(net.ParseIP(c.ip)), "ip=%s", c.ip)
	}
	require.False(t, isPublicIP(nil))
}

// END OPENVIDU BLOCK
