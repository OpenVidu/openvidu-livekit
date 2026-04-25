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
	conf.RTC.NodeIP = "10.0.0.1"
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
}

func TestTURNSecurity_Static_AllowsRelayAddress(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = "10.0.0.1"
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "192.168.1.1"))
}

func TestTURNSecurity_Static_DeniesUnknownIP(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = "10.0.0.1"
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
	conf.RTC.NodeIP = "10.0.0.1"
	conf.ResolvedRelayAddress = "10.0.0.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "10.0.0.1"))
	require.False(t, checkPermission(handler, "10.0.0.2"))
}

func TestTURNSecurity_Static_IPv6(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = "::1"
	conf.ResolvedRelayAddress = "fd00::1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	require.True(t, checkPermission(handler, "::1"))
	require.True(t, checkPermission(handler, "fd00::1"))
	require.False(t, checkPermission(handler, "fd00::2"))
}

func TestTURNSecurity_Static_IPv4MappedIPv6(t *testing.T) {
	conf := &config.Config{}
	conf.RTC.NodeIP = "10.0.0.1"

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

	localIPs, err := rtcconfig.GetLocalIPAddresses(false, nil)
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
	conf.RTC.NodeIP = "10.0.0.1"
	conf.ResolvedRelayAddress = "192.168.1.1"

	s := NewTURNSecurity(conf, nil)
	handler := s.PermissionHandler()

	localIPs, err := rtcconfig.GetLocalIPAddresses(false, nil)
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
	conf.RTC.NodeIP = "10.0.0.1"
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
	gen := newOpenViduRelayAddrGen(nil, 5000, 6000, false)
	require.NotNil(t, gen)
	require.Equal(t, uint16(5000), gen.minPort)
	require.Equal(t, uint16(6000), gen.maxPort)
	require.False(t, gen.standalone)
}

func TestTURNSecurity_OpenViduRelayAddrGen_Standalone(t *testing.T) {
	gen := newOpenViduRelayAddrGen(nil, 0, 0, true)
	require.NotNil(t, gen)
	require.True(t, gen.standalone)
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

func TestTURNSecurity_AllocateConn_TCP_Denied(t *testing.T) {
	gen := newOpenViduRelayAddrGen(nil, 0, 0, false)
	conn, addr, err := gen.AllocateConn("tcp", 0)
	require.Nil(t, conn)
	require.Nil(t, addr)
	require.ErrorIs(t, err, errTCPAllocDenied)
}

func TestTURNSecurity_AllocateConn_TCP_DeniedWithPortRange(t *testing.T) {
	gen := newOpenViduRelayAddrGen(nil, 5000, 6000, true)
	conn, addr, err := gen.AllocateConn("tcp", 5500)
	require.Nil(t, conn)
	require.Nil(t, addr)
	require.ErrorIs(t, err, errTCPAllocDenied)
}

// ---------------------------------------------------------------------------
// extractPort — all branches
// ---------------------------------------------------------------------------

// stringAddr implements net.Addr with a custom string representation,
// exercising the fallback branch in extractPort.
type stringAddr struct {
	network string
	addr    string
}

func (a stringAddr) Network() string { return a.network }
func (a stringAddr) String() string  { return a.addr }

func TestExtractPort_UDPAddr(t *testing.T) {
	port, err := extractPort(&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080})
	require.NoError(t, err)
	require.Equal(t, uint16(8080), port)
}

func TestExtractPort_TCPAddr(t *testing.T) {
	port, err := extractPort(&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 443})
	require.NoError(t, err)
	require.Equal(t, uint16(443), port)
}

func TestExtractPort_FallbackAddr(t *testing.T) {
	port, err := extractPort(stringAddr{"custom", "10.0.0.1:5500"})
	require.NoError(t, err)
	require.Equal(t, uint16(5500), port)
}

func TestExtractPort_FallbackAddr_InvalidFormat(t *testing.T) {
	_, err := extractPort(stringAddr{"custom", "not-a-host-port"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot extract port")
}

func TestExtractPort_FallbackAddr_InvalidPort(t *testing.T) {
	_, err := extractPort(stringAddr{"custom", "10.0.0.1:abc"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot parse port")
}

// END OPENVIDU BLOCK
