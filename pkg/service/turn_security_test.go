// BEGIN OPENVIDU BLOCK
package service

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jxskiss/base62"
	"github.com/pion/turn/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"

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

// ===========================================================================
// TURNAuthHandler — credential format, parsing, hashing and authentication.
// ===========================================================================

func newTestAuthHandler() *TURNAuthHandler {
	return NewTURNAuthHandler(auth.NewSimpleKeyProvider("testkey", "testsecret"))
}

// newTestAuthHandlerAt returns a handler whose clock is pinned to the given time,
// letting expiry tests run without time.Sleep.
func newTestAuthHandlerAt(now time.Time) *TURNAuthHandler {
	h := newTestAuthHandler()
	h.now = func() time.Time { return now }
	return h
}

// ---------------------------------------------------------------------------
// CreateUsername / ParseUsername
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_CreateParseUsername_Roundtrip(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_participant1", time.Time{})
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_participant1"), pID)
	require.True(t, expiry.IsZero(), "zero expiry should produce legacy no-expiry username")
}

func TestTURNAuthHandler_CreateParseUsername_DifferentInputs(t *testing.T) {
	h := newTestAuthHandler()

	tests := []struct {
		apiKey string
		pID    livekit.ParticipantID
	}{
		{"key1", "PA_abc"},
		{"key2", "PA_xyz123"},
		{"k", "PA_p"},
		{"longapikey12345", "PA_longparticipantid67890"},
	}
	for _, tc := range tests {
		username := h.CreateUsername(tc.apiKey, tc.pID, time.Time{})
		gotKey, gotPID, expiry, err := h.ParseUsername(username)
		require.NoError(t, err)
		require.Equal(t, tc.apiKey, gotKey)
		require.Equal(t, tc.pID, gotPID)
		require.True(t, expiry.IsZero())
	}
}

func TestTURNAuthHandler_CreateParseUsername_WithExpiry_Roundtrip(t *testing.T) {
	h := newTestAuthHandler()
	want := time.Unix(1_700_003_600, 0)

	username := h.CreateUsername("testkey", "PA_p1", want)
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_p1"), pID)
	require.False(t, expiry.IsZero())
	require.Equal(t, want.Unix(), expiry.Unix())
}

func TestTURNAuthHandler_ParseUsername_InvalidBase62(t *testing.T) {
	h := newTestAuthHandler()

	_, _, _, err := h.ParseUsername("!!!not-base62!!!")
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_MissingSeparator(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode "noseparator" — no pipe, Split produces 1 part, falls to default → error.
	encoded := base62.EncodeToString([]byte("noseparator"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_TooManyPipes(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode "a|b|c|d" — 4 parts, neither legacy (2) nor expiry-carrying (3).
	encoded := base62.EncodeToString([]byte("a|b|c|d"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_ThreePartForm_BadExpiry(t *testing.T) {
	h := newTestAuthHandler()

	// 3-part form is valid only when the third field parses as int64 unix seconds.
	encoded := base62.EncodeToString([]byte("testkey|PA_p1|notanumber"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_Empty(t *testing.T) {
	h := newTestAuthHandler()
	_, _, _, err := h.ParseUsername("")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// CreatePassword
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_CreatePassword_ValidKey(t *testing.T) {
	h := newTestAuthHandler()

	pw, err := h.CreatePassword("testkey", "PA_participant1", time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, pw)
}

func TestTURNAuthHandler_CreatePassword_Deterministic(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	require.Equal(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_DifferentParticipants(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p2", time.Time{})
	require.NoError(t, err)
	require.NotEqual(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_InvalidKey(t *testing.T) {
	h := newTestAuthHandler()

	_, err := h.CreatePassword("unknownkey", "PA_p1", time.Time{})
	require.ErrorIs(t, err, ErrInvalidAPIKey)
}

// ---------------------------------------------------------------------------
// HandleAuth — basic credential acceptance / rejection
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidCredentials(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_p1", time.Time{})
	key, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
	require.NotNil(t, key)
	require.NotEmpty(t, key)
}

func TestTURNAuthHandler_HandleAuth_InvalidBase62Username(t *testing.T) {
	h := newTestAuthHandler()

	_, ok := h.HandleAuth("!!!invalid!!!", LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_MalformedUsername(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode a string without the pipe separator.
	encoded := base62.EncodeToString([]byte("nopipe"))
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_UnknownAPIKey(t *testing.T) {
	h := newTestAuthHandler()

	// Valid format but the API key is not known to the provider.
	username := h.CreateUsername("unknownkey", "PA_p1", time.Time{})
	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_DifferentParticipantsGetDifferentKeys(t *testing.T) {
	h := newTestAuthHandler()

	u1 := h.CreateUsername("testkey", "PA_p1", time.Time{})
	u2 := h.CreateUsername("testkey", "PA_p2", time.Time{})

	key1, ok1 := h.HandleAuth(u1, LivekitRealm, nil)
	key2, ok2 := h.HandleAuth(u2, LivekitRealm, nil)
	require.True(t, ok1)
	require.True(t, ok2)
	require.NotEqual(t, key1, key2)
}

// ---------------------------------------------------------------------------
// HandleAuth — TTL / expiry
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidFresh(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// Fast-forward clock just past issuance: well before expiry.
	h.now = func() time.Time { return issuedAt.Add(5 * time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidAtBoundary(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// Exactly at expiry — strict After check means still valid.
	h.now = func() time.Time { return issuedAt.Add(time.Hour) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidWithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// 2m past expiry — inside the 5m skew tolerance.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 2*time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ExpiredBeyondSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// 5m + 1s past expiry — beyond skew, must reject.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 5*time.Minute + time.Second) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

// When the operator opts out of TTL (CredentialTTL=0), both username and
// password use the legacy no-expiry form consistently, so authentication
// works indefinitely. This is the documented backward-compat behavior.
func TestTURNAuthHandler_HandleAuth_LegacyModeNeverExpires(t *testing.T) {
	h := newTestAuthHandler()
	username, _, err := h.CreateCredentials("testkey", "PA_p1", 0) // opt-out mode
	require.NoError(t, err)

	// Advance clock arbitrarily far; legacy-mode creds must still authenticate.
	h.now = func() time.Time { return time.Unix(9_999_999_999, 0) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_MalformedExpiry(t *testing.T) {
	h := newTestAuthHandler()

	// 3-part username with a non-numeric expiry — ParseUsername must reject
	// before HandleAuth reaches the hash step.
	encoded := base62.EncodeToString([]byte("testkey|PA_p1|notanumber"))
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok)
}

// ---------------------------------------------------------------------------
// CreateCredentials — atomic (username, password) pair
// ---------------------------------------------------------------------------

// CreateCredentials must emit a username+password that round-trip end-to-end:
// the key HandleAuth returns (derived from the parsed username) must equal the
// key a legitimate client computes with the returned password. If these drift
// the TURN server will reject every legitimate allocation.
func TestTURNAuthHandler_CreateCredentials_RoundTripsThroughHandleAuth(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	for _, ttl := range []time.Duration{0, time.Hour, 24 * time.Hour} {
		username, password, err := h.CreateCredentials("testkey", "PA_p1", ttl)
		require.NoError(t, err)
		require.NotEmpty(t, username)
		require.NotEmpty(t, password)

		serverKey, ok := h.HandleAuth(username, LivekitRealm, nil)
		require.True(t, ok, "ttl=%v", ttl)
		clientKey := turn.GenerateAuthKey(username, LivekitRealm, password)
		require.Equal(t, clientKey, serverKey, "ttl=%v: server/client keys must agree", ttl)
	}
}

func TestTURNAuthHandler_CreateCredentials_UnknownAPIKey(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("unknownkey", "PA_p1", time.Hour)
	require.ErrorIs(t, err, ErrInvalidAPIKey)
}

// ===========================================================================
// TURNAuthHandler — security tests.
//
// Each test models a concrete attacker capability (captured credential,
// wire-layer tampering, parser edge cases) against the long-term credential
// scheme, and asserts that authentication fails end-to-end. Failures here
// indicate an exploitable bypass.
// ===========================================================================

// tamperedAuthRejected returns true when an attacker holding a captured
// (username, password) pair cannot authenticate by replaying the password
// with a tampered username. Either:
//   - HandleAuth rejects the tampered username outright, OR
//   - HandleAuth accepts it at the parse layer but the server-derived key
//     differs from the attacker's replayed key (MESSAGE-INTEGRITY fails in
//     pion/turn).
func tamperedAuthRejected(h *TURNAuthHandler, tamperedUsername string, capturedPassword string) bool {
	serverKey, ok := h.HandleAuth(tamperedUsername, LivekitRealm, nil)
	if !ok {
		return true // rejected at parse / expiry / key-lookup layer
	}
	attackerKey := turn.GenerateAuthKey(tamperedUsername, LivekitRealm, capturedPassword)
	return string(attackerKey) != string(serverKey)
}

func encodeUsername(parts ...string) string {
	var joined string
	for i, p := range parts {
		if i > 0 {
			joined += "|"
		}
		joined += p
	}
	return base62.EncodeToString([]byte(joined))
}

// ---------------------------------------------------------------------------
// Expiry binding
// ---------------------------------------------------------------------------

// Verifies that the expiry is bound into the password hash. Two passwords for
// the same (apiKey, pID) pair but different expiries must differ so an
// attacker who strips the expiry from a leaked 3-part username cannot reuse
// the captured password against the server's legacy code path.
func TestTURNAuthHandler_CreatePassword_ExpiryBoundIntoHash(t *testing.T) {
	h := newTestAuthHandler()

	legacy, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)

	withExpiry, err := h.CreatePassword("testkey", "PA_p1", time.Unix(1_700_000_000, 0))
	require.NoError(t, err)
	require.NotEqual(t, legacy, withExpiry,
		"legacy-vs-expiry password must differ so stripping the expiry from a username "+
			"cannot yield the attacker's captured password")

	differentExpiry, err := h.CreatePassword("testkey", "PA_p1", time.Unix(1_700_003_600, 0))
	require.NoError(t, err)
	require.NotEqual(t, withExpiry, differentExpiry,
		"different expiries must produce different passwords; otherwise rotation by re-expiry would not change the credential")
}

// An attacker captures a legitimate (username, password) pair issued with a
// TTL (3-part username `apiKey|pID|expiry`), base62-decodes the username, and
// re-encodes it as the 2-part legacy form `apiKey|pID` while presenting the
// captured password on the wire. pion/turn verifies MESSAGE-INTEGRITY as
// MD5(wire_username:realm:password), so if the server's recomputed password
// were unchanged by stripping the expiry the auth key would match and the
// leaked credential would authenticate indefinitely, defeating the TTL.
//
// CreatePassword binds the expiry into the hash. On the stripped-username
// path the server recomputes the LEGACY password (expiry zero) while the
// attacker holds the WITH-EXPIRY password. The keys differ and
// MESSAGE-INTEGRITY fails.
func TestTURNAuthHandler_HandleAuth_ExpiryStrippingRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	// Server issues a time-bound credential (3-part username + expiry-bound password).
	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// The legitimate cred authenticates and produces a matching key.
	serverKey, ok := h.HandleAuth(legitUsername, LivekitRealm, nil)
	require.True(t, ok)
	clientKey := turn.GenerateAuthKey(legitUsername, LivekitRealm, legitPassword)
	require.Equal(t, clientKey, serverKey, "legitimate creds must authenticate end-to-end")

	// Attacker strips the expiry, re-encoding as the legacy 2-part form.
	// The attacker still has the captured password (bound to the original expiry).
	strippedUsername := h.CreateUsername("testkey", "PA_p1", time.Time{})

	// The server still accepts the 2-part format at the parse layer...
	serverKeyAttack, attackOk := h.HandleAuth(strippedUsername, LivekitRealm, nil)
	require.True(t, attackOk, "parse layer accepts the 2-part form (this is by design; the real gate is the password hash)")

	// ...but the server's recomputed key uses the LEGACY password (no expiry
	// in hash), while the attacker's key uses the WITH-EXPIRY password.
	// They must differ — otherwise the stripping attack would succeed.
	attackerKey := turn.GenerateAuthKey(strippedUsername, LivekitRealm, legitPassword)
	require.NotEqual(t, attackerKey, serverKeyAttack,
		"SECURITY: stripping the expiry from a leaked 3-part username must produce a password mismatch; "+
			"if these keys are equal, the TTL is bypassable and any leaked credential lasts forever")
}

// Variant of the expiry-stripping scenario: even if the attacker strips
// pre-emptively while the original expiry is still valid (within skew),
// stripping must still produce a mismatched server-side password.
func TestTURNAuthHandler_StripExpiry_WithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	stripped := encodeUsername("testkey", "PA_alice")

	// Move clock to just inside the original expiry — stripping isn't needed for validity,
	// but attacker might try it preemptively before expiry arrives.
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	require.True(t, tamperedAuthRejected(h, stripped, legitPassword),
		"SECURITY: stripped username + captured with-expiry password authenticated even pre-expiry (hash not expiry-bound?)")
}

// ---------------------------------------------------------------------------
// Expiry tampering
// ---------------------------------------------------------------------------

// An attacker who captures a 3-part credential cannot extend its lifetime by
// rewriting the expiry in the username, because the expiry is bound into the
// password hash.
func TestTURNAuthHandler_ExpiryExtensionRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Original expiry was issuedAt + 1h. Attacker tries to rewrite it.
	for _, newExpiry := range []int64{
		issuedAt.Add(100 * time.Hour).Unix(),      // far future
		issuedAt.Add(365 * 24 * time.Hour).Unix(), // a year
		math.MaxInt64,                             // overflow candidate
	} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", newExpiry))
		// Move clock past the original expiry so only tampered expiry could save it.
		h.now = func() time.Time { return issuedAt.Add(2 * time.Hour) }
		require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
			"SECURITY: attacker extended expiry to %d and authenticated — TTL bypassed", newExpiry)
	}

	// sanity: the legitimate creds still authenticate within skew
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	_, ok := h.HandleAuth(legitUsername, LivekitRealm, nil)
	require.True(t, ok)
}

// Attacker rewrites the expiry to values that make Go's `time.Time.IsZero()`
// return true (namely, Unix seconds = -62135596800, which corresponds to
// Go's zero time: Jan 1, year 1 UTC). If the server's parse+hash paths were
// inconsistent here, the expiry check would be bypassed AND the server
// would compute a legacy-shaped password — which matches the legacy
// password an attacker might separately hold.
func TestTURNAuthHandler_ZeroTimeExpiry_NotBypassable(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	// Attacker holds the WITH-EXPIRY password.
	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Jan 1, year 1 UTC — Go's IsZero() returns true for time.Unix(-62135596800, 0).
	tampered := encodeUsername("testkey", "PA_alice", "-62135596800")

	h.now = func() time.Time { return issuedAt.Add(100 * time.Hour) }
	require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
		"SECURITY: zero-time expiry allowed a leaked 3-part password to authenticate indefinitely")
}

// In legacy mode (TTL=0) the operator opts out of expiry. A client encoding a
// 3-part username with the zero-time expiry is equivalent to the 2-part legacy
// form: both derive the same auth key from the legacy password. This test
// documents the invariant so future changes don't accidentally alter it —
// silent rotation breakage would be hard to diagnose.
func TestTURNAuthHandler_ZeroTimeExpiry_LegacyEquivalence(t *testing.T) {
	h := newTestAuthHandler()

	// Operator is running legacy mode (TTL=0). Legit 2-part credential.
	_, legacyPassword, err := h.CreateCredentials("testkey", "PA_alice", 0)
	require.NoError(t, err)

	// Client encodes a 3-part username with expiry = Go's zero time.
	disguised := encodeUsername("testkey", "PA_alice", "-62135596800")

	serverKey, ok := h.HandleAuth(disguised, LivekitRealm, nil)
	require.True(t, ok, "server accepts 3-part-zero form as equivalent to 2-part legacy")
	clientKey := turn.GenerateAuthKey(disguised, LivekitRealm, legacyPassword)
	require.Equal(t, string(clientKey), string(serverKey),
		"legacy password must derive the same auth key under 3-part-zero encoding (consistency invariant)")
}

// ---------------------------------------------------------------------------
// Cross-identity credential reuse
// ---------------------------------------------------------------------------

// Alice cannot use her credential to authenticate as Bob by rewriting pID.
// The pID is bound into the password hash.
func TestTURNAuthHandler_CrossParticipant_PIDForgeryRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, alicePassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Anyone with Alice's captured creds tries to authenticate as someone else
	// by rewriting the pID in the username.
	for _, targetPID := range []string{
		"PA_bob",
		"PA_admin",
		"PA_", // empty suffix
		"",    // empty
		"*",   // wildcard-ish
	} {
		// Preserve the original expiry so the expiry check is not the reason for rejection.
		tampered := encodeUsername("testkey", targetPID, fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
		require.True(t, tamperedAuthRejected(h, tampered, alicePassword),
			"SECURITY: Alice impersonated pID %q using her own captured password", targetPID)
	}
}

// A credential issued against apiKey=K1 must not authenticate against a server
// configured with a different (K1-unknown OR K2-with-different-secret) key.
func TestTURNAuthHandler_CrossAPIKey_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)

	// Server 1: issues credential for Alice under apiKey "K1"/secret "S1".
	h1 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("K1", "S1"),
		now:         func() time.Time { return issuedAt },
	}
	aliceUsername, alicePassword, err := h1.CreateCredentials("K1", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Server 2: same apiKey name, different secret.
	h2 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("K1", "DIFFERENT_SECRET"),
		now:         func() time.Time { return issuedAt.Add(5 * time.Minute) },
	}
	require.True(t, tamperedAuthRejected(h2, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server with a different secret for the same apiKey")

	// Server 3: doesn't know this apiKey at all.
	h3 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("OTHER_KEY", "S1"),
		now:         func() time.Time { return issuedAt.Add(5 * time.Minute) },
	}
	require.True(t, tamperedAuthRejected(h3, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server that doesn't know the apiKey")
}

// ---------------------------------------------------------------------------
// Numeric edge cases on expiry
// ---------------------------------------------------------------------------

// Attacker passes max int64 expiry; time arithmetic inside HandleAuth must not
// panic and must not authenticate without the correct password.
func TestTURNAuthHandler_MaxInt64Expiry_NoPanicNoBypass(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", int64(math.MaxInt64)))

	require.NotPanics(t, func() {
		require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
			"SECURITY: MaxInt64 expiry allowed captured password to authenticate")
	}, "MaxInt64 expiry must not panic on clock arithmetic")
}

// Attacker passes a negative expiry. Server must reject (now.After == true)
// AND captured password must not match.
func TestTURNAuthHandler_NegativeExpiry_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	for _, negExpiry := range []int64{-1, -1000, math.MinInt64} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", negExpiry))
		require.NotPanics(t, func() {
			require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
				"SECURITY: negative expiry %d allowed captured password to authenticate", negExpiry)
		})
	}
}

// ---------------------------------------------------------------------------
// Username byte-layer tampering
// ---------------------------------------------------------------------------

// Attacker injects a null byte into the pID. Must not match any legitimate pID.
func TestTURNAuthHandler_NullByteInPID_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice\x00", fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
	require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
		"SECURITY: null-byte injection in pID allowed captured password to authenticate")
}

// Attacker attempts to craft a username whose decoded plaintext contains a
// `|` within the pID, producing an ambiguous 4-part split. Server must reject.
func TestTURNAuthHandler_PipeInjectionInPID_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	// 4-part plaintext: "apiKey|PA_foo|PA_bar|1700000000". ParseUsername must
	// treat this as invalid.
	encoded := encodeUsername("testkey", "PA_foo", "PA_bar", "1700000000")
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok, "SECURITY: ambiguous 4-part username was accepted")
}

// Attacker attempts 1-part and 5+ part forms.
func TestTURNAuthHandler_WrongNumberOfParts_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	cases := []struct {
		name  string
		parts []string
	}{
		{"1-part", []string{"loneword"}},
		{"5-part", []string{"a", "b", "c", "d", "e"}},
		{"6-part", []string{"a", "b", "c", "d", "e", "f"}},
		{"empty-single", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			encoded := encodeUsername(c.parts...)
			_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
			require.False(t, ok, "SECURITY: %s username was accepted", c.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Replay & empty-field smuggling
// ---------------------------------------------------------------------------

// Pure replay of the captured credential after expiry+skew must fail.
func TestTURNAuthHandler_ReplayAfterExpiry_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Advance beyond skew.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + turnExpirySkew + time.Second) }
	require.True(t, tamperedAuthRejected(h, legitUsername, legitPassword),
		"SECURITY: pure replay past expiry+skew authenticated (TTL not enforced)")
}

// If the server treated empty pID as a wildcard or looked up a default secret
// under the empty apiKey, this would be catastrophic. Verify neither happens.
func TestTURNAuthHandler_EmptyFields_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	// Capture a legit credential for a real participant.
	_, realPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Try various empty-field combinations.
	expiryStr := fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix())
	cases := [][]string{
		{"", "PA_alice", expiryStr}, // empty apiKey
		{"testkey", "", expiryStr},  // empty pID
		{"", "", expiryStr},         // both empty
		{"", "PA_alice"},            // 2-part empty apiKey
		{"testkey", ""},             // 2-part empty pID
		{"", ""},                    // 2-part both empty
	}
	for i, parts := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			tampered := encodeUsername(parts...)
			require.True(t, tamperedAuthRejected(h, tampered, realPassword),
				"SECURITY: empty-field form %v authenticated with real password", parts)
		})
	}
}

// ---------------------------------------------------------------------------
// Operator misconfiguration must fail loudly, not silently
// ---------------------------------------------------------------------------

// If the operator configures an apiKey or pID containing the '|' separator or
// a NUL byte, CreateCredentials must refuse rather than mint a credential
// that won't round-trip. Without this validation the misconfiguration would
// only surface at TURN connect time as opaque auth failures.

func TestTURNAuthHandler_RejectsSeparatorInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key|with|pipe", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key|with|pipe", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsSeparatorInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice|bob"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsNullByteInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key\x00nul", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key\x00nul", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsNullByteInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice\x00"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

// Normal inputs continue to work after the validation.
func TestTURNAuthHandler_AcceptsNormalInputs(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)
	_, _, err = h.CreateCredentials("testkey", "PA_alice", 0) // legacy
	require.NoError(t, err)
}

// ===========================================================================
// resolveTURNRelayAddress
// ===========================================================================

func TestResolveTURNRelayAddress(t *testing.T) {
	t.Run("explicit RelayAddress is always used", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("explicit RelayAddress used even when not publicly reachable", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = "192.168.1.1"
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("publicly reachable with no RelayAddress falls back to NodeIP", func(t *testing.T) {
		conf := &config.Config{}
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "203.0.113.1", addr)
	})

	t.Run("not publicly reachable with no RelayAddress uses first local IP", func(t *testing.T) {
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, nil)
		if err != nil {
			t.Skipf("could not get local IP addresses: %v", err)
		}
		if len(localIPs) == 0 {
			t.Skip("no local IP addresses found")
		}

		conf := &config.Config{}
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, localIPs[0], addr)
	})
}

// ===========================================================================
// extractPort — all branches
// ===========================================================================

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
