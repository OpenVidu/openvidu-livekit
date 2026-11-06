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

	"github.com/pion/turn/v5"
	"github.com/redis/go-redis/v9"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-server/openvidu/customrouting"
	"github.com/livekit/livekit-server/pkg/config"
)

// defaultTURNCacheTTL is how long allowed IPs from Redis are cached before
// a periodic refresh is forced. Cache misses trigger an immediate refresh
// regardless of TTL, so new nodes are visible instantly.
const defaultTURNCacheTTL = time.Minute

// negativeRevalidateInterval bounds how often a "miss" against a freshly
// refreshed snapshot is revalidated with another Redis fetch. A newer snapshot
// that does not contain the peer IP is revalidated (it might be a transient gap
// in nodes_openvidu), but only once it is at least this old — so a burst of
// concurrent misses for the same genuinely-absent IP reuses the snapshot instead
// of each issuing its own fetch, preventing a fetch storm.
const negativeRevalidateInterval = 100 * time.Millisecond

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

	// CIDR rules from the TURN config (AllowRestrictedPeerCIDRs /
	// DenyPeerCIDRs), already compiled by NewTurnServer with parsePeerCIDRs
	// so a malformed entry fails startup instead of being skipped. They are
	// enforced inside handlePermission so that the OpenVidu cluster-node allow
	// logic never overrides the configured deny/allow policy.
	allowNets []*net.IPNet
	denyNets  []*net.IPNet

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
// allowNets / denyNets are conf.TURN.AllowRestrictedPeerCIDRs / DenyPeerCIDRs as
// compiled by parsePeerCIDRs (NewTurnServer rejects malformed entries before
// getting here).
func NewTURNSecurity(conf *config.Config, rc redis.UniversalClient, allowNets, denyNets []*net.IPNet) *TURNSecurity {
	s := &TURNSecurity{
		rc:        rc,
		cacheTTL:  defaultTURNCacheTTL,
		allowNets: allowNets,
		denyNets:  denyNets,
	}

	// Always discover this machine's local IPs.
	localIPs := make(map[string]struct{})
	ips, _ := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
	for _, ip := range ips {
		if ip != "" {
			localIPs[ip] = struct{}{}
		}
	}
	s.localIPs = localIPs

	if rc == nil {
		allowed := make(map[string]struct{}, 2)
		if conf.RTC.NodeIP.V4 != "" {
			allowed[conf.RTC.NodeIP.V4] = struct{}{}
		}
		if conf.RTC.NodeIP.V6 != "" {
			allowed[conf.RTC.NodeIP.V6] = struct{}{}
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
	return s.handlePermissionWithCIDRs
}

func (s *TURNSecurity) handlePermissionWithCIDRs(clientAddr net.Addr, peerIP net.IP) bool {
	peerStr := peerIP.String()

	// Deny CIDRs always take precedence — over the allow list below and over
	// local/cluster IPs.
	for _, ipnet := range s.denyNets {
		if ipnet.Contains(peerIP) {
			logger.Infow("TURN permission denied by deny CIDR", "peerIP", peerStr)
			return false
		}
	}

	if len(s.allowNets) > 0 {
		// A peer IP explicitly listed in allow_restricted_peer_cidrs is permitted,
		// regardless of whether it is private, public, or a cluster node. This is
		// an explicit operator override (deny CIDRs above still take precedence).
		for _, ipnet := range s.allowNets {
			if ipnet.Contains(peerIP) {
				return true
			}
		}
		// With an allow list configured, restricted peer IPs that are NOT listed
		// are denied — the allow list narrows which restricted ranges may be used.
		// Non-restricted (public) IPs that are not listed fall through to the
		// cluster/local allowlist below. Restricted is upstream's definition
		// (isRestrictedPeerIP, turnrelay.go).
		if isRestrictedPeerIP(peerIP) {
			logger.Infow("TURN permission denied: restricted IP not in allow CIDRs", "peerIP", peerStr)
			return false
		}
	}

	return s.handlePermission(clientAddr, peerIP)
}

func (s *TURNSecurity) handlePermission(_ net.Addr, peerIP net.IP) bool {
	peerStr := peerIP.String()

	// Fast path: local interface IPs are always allowed (the TURN relay
	// is embedded and may need to forward to any local interface).
	if _, ok := s.localIPs[peerStr]; ok {
		return true
	}

	// If no Redis is configured, check directly against the static allow list.
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
	allowed, err := s.refreshCache(cacheTime, peerStr)
	if err != nil {
		logger.Warnw("TURN permission denied: failed to query cluster nodes from Redis", err, "peerIP", peerStr)
		return false
	}

	_, ok := allowed[peerStr]
	if !ok {
		if isPublicIP(peerIP) {
			// A relay attempt toward a non-cluster PUBLIC IP is a genuine security
			// signal: the embedded relay must not act as an open proxy. Surface it.
			logger.Warnw("TURN permission denied: peer IP not in cluster nodes", nil, "peerIP", peerStr)
		} else {
			// A private/link-local peer IP is almost always a cluster node that is
			// transiently absent from nodes_openvidu during churn (benign). Keep it
			// at info to avoid warning-level noise.
			logger.Infow("TURN permission denied: peer IP not in cluster nodes", "peerIP", peerStr)
		}
	}
	return ok
}

// isPublicIP reports whether ip is a globally-routable address (not private,
// loopback, link-local or unspecified). Used to decide whether a denied relay
// peer is a genuine external target (worth a warning) or a benign in-cluster
// address transiently missing from nodes_openvidu.
func isPublicIP(ip net.IP) bool {
	return ip != nil &&
		ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast()
}

// refreshCache fetches the allowed IPs from Redis and updates the cache.
// prevCacheTime deduplicates concurrent refreshes, but only for a POSITIVE
// result: if another goroutine already refreshed after prevCacheTime and the
// requested peerStr is present in that snapshot, it is reused. If peerStr is
// absent, the newer snapshot may have been captured during a transient
// inconsistency in nodes_openvidu (e.g. the brief window while a node IP is
// recycled), so we fall through to a fresh fetch rather than deny based on an
// unvalidated snapshot — but that negative revalidation is rate-limited by
// negativeRevalidateInterval so a burst of misses for the same absent IP cannot
// cause a fetch storm.
func (s *TURNSecurity) refreshCache(prevCacheTime time.Time, peerStr string) (map[string]struct{}, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Another goroutine may have refreshed while we waited for the lock.
	if s.cacheTime.After(prevCacheTime) {
		// Positive result: a newer snapshot confirms the IP — reuse it.
		if _, ok := s.cachedIPs[peerStr]; ok {
			return s.cachedIPs, nil
		}
		// Negative result: revalidate with a fresh fetch only if the snapshot is
		// old enough that Redis may have changed; otherwise reuse it to bound
		// refetch load under a burst of misses for the same absent IP.
		if time.Since(s.cacheTime) < negativeRevalidateInterval {
			return s.cachedIPs, nil
		}
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
// OpenVidu relay port policy
// ---------------------------------------------------------------------------

// openviduRelayPortPolicy extends upstream's turnRelayPolicy (turnrelay.go) to
// every relay peer. Upstream limits relays to this host's own addresses to the
// RTC media ports, because those packets bypass any firewall in front of the
// host. TURNSecurity also lets the relay reach the other cluster nodes, whose
// private addresses sit behind the same firewall, so relays to any peer may only
// reach the media ports. Like upstream, packets to other ports are dropped.
// Upstream's rejectTCPRelayAddressGenerator, wrapped inside, still refuses
// RFC 6062 TCP relays.
type openviduRelayPortPolicy struct {
	turn.RelayAddressGenerator
	mediaPorts []portRange
}

func newOpenViduRelayPortPolicy(g turn.RelayAddressGenerator, mediaPorts []portRange) *openviduRelayPortPolicy {
	return &openviduRelayPortPolicy{RelayAddressGenerator: g, mediaPorts: mediaPorts}
}

func (g *openviduRelayPortPolicy) AllocatePacketConn(c turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	conn, addr, err := g.RelayAddressGenerator.AllocatePacketConn(c)
	if err != nil {
		return conn, addr, err
	}
	return &openviduRelayPacketConn{PacketConn: conn, mediaPorts: g.mediaPorts}, addr, nil
}

type openviduRelayPacketConn struct {
	net.PacketConn
	mediaPorts []portRange
}

func (c *openviduRelayPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if !isMediaPort(addr, c.mediaPorts) {
		// Expected and high-volume: ICE probes reflexive/relay candidates on
		// ephemeral ports outside the media range — mostly the cluster nodes' own
		// srflx candidates (public IP : NAT-mapped port) for intra-cluster links.
		logger.Debugw("TURN relay destination port outside the media ports, dropping packet",
			"destAddr", addr.String(), "mediaPorts", formatPortRanges(c.mediaPorts))
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

func isMediaPort(addr net.Addr, mediaPorts []portRange) bool {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return false
	}
	port := udpAddr.AddrPort().Port()
	for _, r := range mediaPorts {
		if port >= r.start && port <= r.end {
			return true
		}
	}
	return false
}

func formatPortRanges(ranges []portRange) []string {
	formatted := make([]string, 0, len(ranges))
	for _, r := range ranges {
		formatted = append(formatted, fmt.Sprintf("%d-%d", r.start, r.end))
	}
	return formatted
}

func logOpenViduRelayPortPolicy(mediaPorts []portRange) {
	if len(mediaPorts) == 0 {
		logger.Warnw("TURN relay peer port restriction: no RTC media UDP ports configured, every relayed packet will be dropped", nil)
		return
	}
	logger.Infow("TURN relay peer port restriction enabled", "mediaPorts", formatPortRanges(mediaPorts))
}

// END OPENVIDU BLOCK
