// BEGIN OPENVIDU BLOCK
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

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/turn/v4"
	"github.com/redis/go-redis/v9"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-server/openvidu/customrouting"
	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
)

// defaultTURNCacheTTL is how long allowed IPs from Redis are cached before
// a periodic refresh is forced. Cache misses trigger an immediate refresh
// regardless of TTL, so new nodes are visible instantly.
const defaultTURNCacheTTL = time.Minute

// TURNSecurity restricts TURN relay connections so that only peer IPs
// belonging to registered cluster nodes or the local machine are allowed.
//
// Local IPs (all non-loopback interfaces) are discovered once at startup
// and always permitted — the embedded TURN relay may need to forward to
// any local interface (e.g. Docker bridge) when ICE picks that path.
//
// With Redis: queries the nodes_openvidu hash for remote cluster node
// IPs, caching results for up to defaultTURNCacheTTL. A cache miss
// triggers an immediate refresh for instant new-node visibility.
//
// Without Redis (dev): uses only the local IPs plus
// the configured NodeIP and ResolvedRelayAddress.
type TURNSecurity struct {
	rc       redis.UniversalClient
	localIPs map[string]struct{} // this machine's IPs — always checked first
	allowed  map[string]struct{} // static set for no-Redis mode (NodeIP + RelayAddress)

	// Redis-mode cache: refreshed on cache miss or TTL expiry.
	cacheMu   sync.RWMutex
	cachedIPs map[string]struct{}
	cacheTime time.Time
	cacheTTL  time.Duration
}

// NewTURNSecurity creates a TURNSecurity instance.
// Local IPs are always discovered at startup.
// When rc is non-nil, remote cluster node IPs are queried from Redis.
// When rc is nil, only local IPs and the configured NodeIP/RelayAddress are used.
func NewTURNSecurity(conf *config.Config, rc redis.UniversalClient) *TURNSecurity {
	s := &TURNSecurity{rc: rc, cacheTTL: defaultTURNCacheTTL}

	// Always discover this machine's local IPs.
	localIPs := make(map[string]struct{})
	ips, _ := rtcconfig.GetLocalIPAddresses(false, nil)
	for _, ip := range ips {
		if ip != "" {
			localIPs[ip] = struct{}{}
		}
	}
	s.localIPs = localIPs

	if rc == nil {
		allowed := make(map[string]struct{}, 2)
		if conf.RTC.NodeIP != "" {
			allowed[conf.RTC.NodeIP] = struct{}{}
		}
		if conf.ResolvedRelayAddress != "" {
			allowed[conf.ResolvedRelayAddress] = struct{}{}
		}
		s.allowed = allowed
		logger.Infow("TURN security using static mode", "allowed", allowed, "localIPs", localIPs)
	} else {
		logger.Infow("TURN security using Redis mode", "localIPs", localIPs)
	}

	return s
}

// PermissionHandler returns a turn.PermissionHandler suitable for use in
// ListenerConfig / PacketConnConfig.
func (s *TURNSecurity) PermissionHandler() turn.PermissionHandler {
	return s.handlePermission
}

func (s *TURNSecurity) handlePermission(_ net.Addr, peerIP net.IP) bool {
	peerStr := peerIP.String()

	// Fast path: local interface IPs are always allowed (the TURN relay
	// is embedded and may need to forward to any local interface).
	if _, ok := s.localIPs[peerStr]; ok {
		return true
	}

	if s.rc == nil {
		_, ok := s.allowed[peerStr]
		if !ok {
			logger.Infow("TURN permission denied: peer IP not in local node", "peerIP", peerStr)
		}
		return ok
	}

	// Read cache.
	s.cacheMu.RLock()
	cached, cacheTime := s.cachedIPs, s.cacheTime
	fresh := cached != nil && time.Since(cacheTime) < s.cacheTTL
	var inCache bool
	if fresh {
		_, inCache = cached[peerStr]
	}
	s.cacheMu.RUnlock()

	// Cache hit: IP found in a fresh cache.
	if fresh && inCache {
		return true
	}

	// Cache miss or expired: refresh from Redis.
	allowed, err := s.refreshCache(cacheTime)
	if err != nil {
		logger.Warnw("TURN permission denied: failed to query cluster nodes from Redis", err, "peerIP", peerStr)
		return false
	}

	_, ok := allowed[peerStr]
	if !ok {
		logger.Infow("TURN permission denied: peer IP not in cluster nodes", "peerIP", peerStr)
	}
	return ok
}

// refreshCache fetches the allowed IPs from Redis and updates the cache.
// prevCacheTime is used to deduplicate concurrent refreshes: if another
// goroutine already refreshed after prevCacheTime, its result is reused.
func (s *TURNSecurity) refreshCache(prevCacheTime time.Time) (map[string]struct{}, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Another goroutine may have refreshed while we waited for the lock.
	if s.cacheTime.After(prevCacheTime) {
		return s.cachedIPs, nil
	}

	allowed, err := s.fetchAllowedIPs()
	if err != nil {
		// Serve stale cache during Redis outage to keep TURN working.
		// Update cacheTime to avoid hammering a dead Redis on every hit;
		// misses will still retry Redis immediately (fast recovery).
		if s.cachedIPs != nil {
			logger.Warnw("TURN cache: serving stale data due to Redis error", err)
			s.cacheTime = time.Now()
			return s.cachedIPs, nil
		}
		return nil, err
	}
	s.cachedIPs = allowed
	s.cacheTime = time.Now()
	return allowed, nil
}

// fetchAllowedIPs queries the nodes_openvidu hash from Redis and returns
// the set of allowed IPs.
func (s *TURNSecurity) fetchAllowedIPs() (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := s.rc.HGetAll(ctx, customrouting.NodesOpenViduKey).Result()
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{}, len(raw)*2)
	for _, v := range raw {
		var node customrouting.NodeOpenVidu
		if err := json.Unmarshal([]byte(v), &node); err != nil {
			logger.Warnw("failed to unmarshal NodeOpenVidu during TURN permission check", err, "raw", v)
			continue
		}
		if node.NodeIp != "" {
			allowed[node.NodeIp] = struct{}{}
		}
		if node.RelayAddress != "" {
			allowed[node.RelayAddress] = struct{}{}
		}
	}

	return allowed, nil
}

// ---------------------------------------------------------------------------
// OpenVidu relay address generator (port restriction + optional telemetry)
// ---------------------------------------------------------------------------

// openviduRelayPacketConn wraps a net.PacketConn with:
//   - Port restriction: rejects WriteTo to destination ports outside [minPort, maxPort].
//     When both are 0 (no ICE port range configured), all peer ports are denied.
//   - Prometheus telemetry: when standalone is true, counts bytes/packets on
//     WriteTo/ReadFrom and tracks connection count on Close.
type openviduRelayPacketConn struct {
	net.PacketConn
	minPort    uint16
	maxPort    uint16
	standalone bool
}

func (c *openviduRelayPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	port, err := extractPort(addr)
	if err != nil {
		return 0, err
	}
	if port < c.minPort || port > c.maxPort {
		portErr := fmt.Errorf("destination port %d outside allowed range [%d, %d]", port, c.minPort, c.maxPort)
		logger.Warnw("Permission denied: destination port outside allowed range", portErr, "destAddr", addr.String(), "port", port, "minPort", c.minPort, "maxPort", c.maxPort)
		return 0, portErr
	}

	n, err := c.PacketConn.WriteTo(p, addr)

	if c.standalone && n > 0 {
		prometheus.IncrementBytes("", prometheus.Outgoing, uint64(n), false)
		prometheus.IncrementPackets("", prometheus.Outgoing, 1, false)
	}
	return n, err
}

func (c *openviduRelayPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if c.standalone && n > 0 {
		prometheus.IncrementBytes("", prometheus.Incoming, uint64(n), false)
		prometheus.IncrementPackets("", prometheus.Incoming, 1, false)
	}
	return n, addr, err
}

func (c *openviduRelayPacketConn) Close() error {
	if c.standalone {
		prometheus.SubConnection(prometheus.Outgoing)
	}
	return c.PacketConn.Close()
}

func extractPort(addr net.Addr) (uint16, error) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return uint16(a.Port), nil
	case *net.TCPAddr:
		return uint16(a.Port), nil
	default:
		_, portStr, err := net.SplitHostPort(addr.String())
		if err != nil {
			return 0, fmt.Errorf("cannot extract port from address %v: %w", addr, err)
		}
		var port int
		_, err = fmt.Sscanf(portStr, "%d", &port)
		if err != nil {
			return 0, fmt.Errorf("cannot parse port %q: %w", portStr, err)
		}
		return uint16(port), nil
	}
}

// openviduRelayAddrGen wraps a turn.RelayAddressGenerator with port restriction
// and optional prometheus telemetry for standalone mode.
type openviduRelayAddrGen struct {
	inner      turn.RelayAddressGenerator
	minPort    uint16
	maxPort    uint16
	standalone bool
}

func newOpenViduRelayAddrGen(inner turn.RelayAddressGenerator, minPort, maxPort uint16, standalone bool) *openviduRelayAddrGen {
	return &openviduRelayAddrGen{
		inner:      inner,
		minPort:    minPort,
		maxPort:    maxPort,
		standalone: standalone,
	}
}

func (g *openviduRelayAddrGen) Validate() error {
	return g.inner.Validate()
}

func (g *openviduRelayAddrGen) AllocatePacketConn(network string, requestedPort int) (net.PacketConn, net.Addr, error) {
	conn, addr, err := g.inner.AllocatePacketConn(network, requestedPort)
	if err != nil {
		logger.Warnw("TURN AllocatePacketConn failed", err,
			"network", network,
			"requestedPort", requestedPort,
		)
		return nil, nil, err
	}

	if g.standalone {
		prometheus.AddConnection(prometheus.Outgoing)
	}
	return &openviduRelayPacketConn{
		PacketConn: conn,
		minPort:    g.minPort,
		maxPort:    g.maxPort,
		standalone: g.standalone,
	}, addr, nil
}

// AllocateConn handles TCP relay allocation requests (RFC 6062).
// Always denied: pion/turn v4 does not implement RFC 6062 server-side — all
// built-in RelayAddressGenerators return errTODO and the server allocation
// path never calls AllocateConn. This method exists only to satisfy the
// turn.RelayAddressGenerator interface.
// If a future pion/turn version adds RFC 6062 support, this should be
// revisited: a net.Conn wrapper with port restriction and telemetry
// (analogous to openviduRelayPacketConn) would be needed.
func (g *openviduRelayAddrGen) AllocateConn(string, int) (net.Conn, net.Addr, error) {
	logger.Infow("TURN AllocateConn denied: TCP relay allocations are not permitted")
	return nil, nil, errTCPAllocDenied
}

var errTCPAllocDenied = fmt.Errorf("TCP relay allocations are not permitted")

// END OPENVIDU BLOCK
