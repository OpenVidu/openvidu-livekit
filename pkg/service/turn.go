// Copyright 2023 LiveKit, Inc.
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
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jxskiss/base62"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v5"
	"github.com/pkg/errors"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/pionlogger"

	// BEGIN OPENVIDU BLOCK
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/redis/go-redis/v9"

	// END OPENVIDU BLOCK

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/telemetry"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
)

const (
	LivekitRealm = "livekit"

	allocateRetries = 50
)

var ErrExpired = errors.New("expired")

// parsePeerCIDRs compiles a list of CIDR strings, failing with a field-specific
// error on any invalid entry so a malformed peer policy is never silently ignored.
func parsePeerCIDRs(field string, cidrs []string) ([]*net.IPNet, error) {
	parsed := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q in %s: %w", cidr, field, err)
		}
		parsed = append(parsed, ipnet)
	}
	return parsed, nil
}

// BEGIN OPENVIDU BLOCK — added rc parameter for TURNSecurity
func NewTurnServer(conf *config.Config, authHandler turn.AuthHandler, standalone bool, rc redis.UniversalClient) (*turn.Server, error) {
	// END OPENVIDU BLOCK
	turnConf := conf.TURN

	// BEGIN OPENVIDU BLOCK
	relayAddress, err := resolveTURNRelayAddress(conf)
	if err != nil {
		return nil, err
	}
	conf.ResolvedRelayAddress = relayAddress
	logger.Infow("TURN relay address configured", "relayAddress", conf.ResolvedRelayAddress)
	// END OPENVIDU BLOCK

	if !turnConf.Enabled {
		return nil, nil
	}

	if turnConf.TLSPort <= 0 && turnConf.UDPPort <= 0 {
		return nil, errors.New("invalid TURN ports")
	} else if turnConf.TLSPort > 0 {
		if turnConf.Domain == "" {
			return nil, errors.New("TURN domain required")
		}

		if !IsValidDomain(turnConf.Domain) {
			return nil, errors.New("TURN domain is not correct")
		}
	}

	// BEGIN OPENVIDU BLOCK
	if conf.RTC.ICEPortRangeStart != 0 && conf.RTC.ICEPortRangeEnd != 0 {
		logger.Infow("TURN relay peer port restriction enabled",
			"minPort", conf.RTC.ICEPortRangeStart,
			"maxPort", conf.RTC.ICEPortRangeEnd,
		)
	} else {
		logger.Warnw("TURN relay peer port restriction: no ICE port range configured, all peer relay ports will be denied", nil)
	}

	if turnConf.EnableRFC6062 {
		logger.Infow("TURN RFC 6062 (TCP allocations) enabled")
	} else {
		logger.Infow("TURN RFC 6062 (TCP allocations) disabled — clients can only allocate UDP relays")
	}
	// END OPENVIDU BLOCK

	// parse peer CIDR policies once at startup so a malformed entry fails loudly
	// instead of being silently skipped on every permission decision (fail-open)
	allowRestrictedPeerCIDRs, err := parsePeerCIDRs("turn.allow_restricted_peer_cidrs", turnConf.AllowRestrictedPeerCIDRs)
	if err != nil {
		return nil, err
	}
	denyPeerCIDRs, err := parsePeerCIDRs("turn.deny_peer_cidrs", turnConf.DenyPeerCIDRs)
	if err != nil {
		return nil, err
	}

	serverConfig := turn.ServerConfig{
		Realm:         LivekitRealm,
		AuthHandler:   authHandler,
		LoggerFactory: pionlogger.NewLoggerFactory(logger.GetLogger()),
	}

	// cap concurrent relay allocations per participant so one credential cannot
	// exhaust the shared relay-port range (a value <= 0 disables the quota)
	if turnConf.PerUserRelayAllocationLimit > 0 {
		quota := newTURNAllocationQuota(turnConf.PerUserRelayAllocationLimit)
		serverConfig.QuotaHandler = quota.Allow
		serverConfig.EventHandler = quota.eventHandler()
	}

	var logValues []any
	logValues = append(logValues, "turn.relay_range_start", turnConf.RelayPortRangeStart)
	logValues = append(logValues, "turn.relay_range_end", turnConf.RelayPortRangeEnd)
	logValues = append(logValues, "turn.per_user_relay_allocation_limit", turnConf.PerUserRelayAllocationLimit)

	// BEGIN OPENVIDU BLOCK
	// Restrict TURN relay peers to this machine's local IPs and the registered
	// cluster-node IPs (Redis nodes_openvidu). Without this the embedded relay
	// is an open proxy to any public IP for anyone holding valid TURN
	// credentials. TURNSecurity also enforces the AllowRestrictedPeerCIDRs /
	// DenyPeerCIDRs rules parsed above, superseding the inline permission
	// handler. Built once and shared across bind addresses.
	turnSecurity := NewTURNSecurity(conf, rc, allowRestrictedPeerCIDRs, denyPeerCIDRs)
	// END OPENVIDU BLOCK

	for _, addr := range turnConf.BindAddresses {
		// BEGIN OPENVIDU BLOCK
		// Use the resolved relay address (which may come from explicit config,
		// preferred interface, or NodeIP) instead of per-bind nodeIP.
		effectiveRelayIP := relayAddress
		if effectiveRelayIP == "" {
			if net.ParseIP(addr).To4() != nil {
				effectiveRelayIP = conf.RTC.NodeIP.V4
			} else {
				effectiveRelayIP = conf.RTC.NodeIP.V6
			}
		}
		// END OPENVIDU BLOCK
		if effectiveRelayIP == "" {
			return nil, errors.New("no matching node IP for relay")
		}

		var relayAddrGen turn.RelayAddressGenerator = &turn.RelayAddressGeneratorPortRange{
			// BEGIN OPENVIDU BLOCK
			RelayAddress: net.ParseIP(effectiveRelayIP),
			// END OPENVIDU BLOCK
			Address:    addr,
			MinPort:    turnConf.RelayPortRangeStart,
			MaxPort:    turnConf.RelayPortRangeEnd,
			MaxRetries: allocateRetries,
		}
		// BEGIN OPENVIDU BLOCK
		relayAddrGen = newOpenViduRelayAddrGen(
			relayAddrGen,
			uint16(conf.RTC.ICEPortRangeStart),
			uint16(conf.RTC.ICEPortRangeEnd),
			standalone,
			turnConf.EnableRFC6062,
		)
		// END OPENVIDU BLOCK

		// BEGIN OPENVIDU BLOCK
		// Enforce the cluster-node peer-IP allowlist (plus the configured
		// AllowRestrictedPeerCIDRs / DenyPeerCIDRs rules). This replaces the
		// previous inline handler, which allowed relaying to any public IP.
		permissionHandler := turnSecurity.PermissionHandler()
		// END OPENVIDU BLOCK

		if turnConf.TLSPort > 0 {
			var listener net.Listener
			var listenerErr error

			if turnConf.ExternalTLS {
				listener, listenerErr = net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(turnConf.TLSPort)))
			} else {
				cert, err := tls.LoadX509KeyPair(turnConf.CertFile, turnConf.KeyFile)
				if err != nil {
					return nil, errors.Wrap(err, "TURN tls cert required")
				}

				listener, listenerErr = tls.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(turnConf.TLSPort)),
					&tls.Config{
						MinVersion:   tls.VersionTLS12,
						Certificates: []tls.Certificate{cert},
					})
			}

			if listenerErr != nil {
				return nil, errors.Wrap(listenerErr, "could not listen on TURN TCP port")
			}
			if standalone {
				listener = telemetry.NewListener(listener)
			}

			listenerConfig := turn.ListenerConfig{
				Listener:              listener,
				RelayAddressGenerator: relayAddrGen,
				PermissionHandler:     permissionHandler,
			}
			serverConfig.ListenerConfigs = append(serverConfig.ListenerConfigs, listenerConfig)

			logValues = append(logValues, "turn.portTLS", turnConf.TLSPort, "turn.externalTLS", turnConf.ExternalTLS)
		}

		if turnConf.UDPPort > 0 {
			udpListener, err := net.ListenPacket("udp", net.JoinHostPort(addr, strconv.Itoa(turnConf.UDPPort)))
			if err != nil {
				return nil, errors.Wrap(err, "could not listen on TURN UDP port")
			}

			if standalone {
				udpListener = telemetry.NewPacketConn(udpListener, prometheus.Incoming)
			}

			packetConfig := turn.PacketConnConfig{
				PacketConn:            udpListener,
				RelayAddressGenerator: relayAddrGen,
				PermissionHandler:     permissionHandler,
			}
			serverConfig.PacketConnConfigs = append(serverConfig.PacketConnConfigs, packetConfig)
			logValues = append(logValues, "turn.portUDP", turnConf.UDPPort)
		}
	}

	logger.Infow("Starting TURN server", logValues...)
	return turn.NewServer(serverConfig)
}

// BEGIN OPENVIDU BLOCK
func resolveTURNRelayAddress(conf *config.Config) (string, error) {
	if conf.TURN.RelayAddress != "" {
		return conf.TURN.RelayAddress, nil
	}
	if !conf.PubliclyReachable {
		var preferredInterfaces []string
		if conf.TURN.RelayPreferredInterface != "" {
			preferredInterfaces = []string{conf.TURN.RelayPreferredInterface}
		}
		var ifFilter func(string) bool
		if len(preferredInterfaces) > 0 {
			ifFilter = func(name string) bool {
				for _, iface := range preferredInterfaces {
					if name == iface {
						return true
					}
				}
				return false
			}
		}
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, ifFilter, nil)
		if err != nil {
			return "", errors.Wrap(err, "could not get local IP addresses for TURN relay")
		}
		if len(localIPs) > 0 {
			logger.Infow("Using first local IP as TURN relay address",
				"relayAddress", localIPs[0],
				"preferredInterface", conf.TURN.RelayPreferredInterface,
			)
			return localIPs[0], nil
		}
	}
	return conf.RTC.NodeIP.PrimaryIP(), nil
}

// END OPENVIDU BLOCK

func getTURNAuthHandlerFunc(handler *TURNAuthHandler) turn.AuthHandler {
	return handler.HandleAuth
}

type TURNAuthHandler struct {
	keyProvider auth.KeyProvider
}

func NewTURNAuthHandler(keyProvider auth.KeyProvider) *TURNAuthHandler {
	return &TURNAuthHandler{
		keyProvider: keyProvider,
	}
}

func (h *TURNAuthHandler) CreateUsername(apiKey string, pID livekit.ParticipantID, ttlSeconds int) (string, int64) {
	// clamp defensively: non-positive TTLs fall back to the default and overflowing ones are capped
	ttlSeconds, _ = config.ClampTURNTTLSeconds(ttlSeconds)
	expiry := time.Now().Add(time.Duration(ttlSeconds) * time.Second).Unix()
	return base62.EncodeToString(fmt.Appendf(nil, "%s|%s|%d", apiKey, pID, expiry)), expiry
}

func (h *TURNAuthHandler) ParseUsername(username string) (string, livekit.ParticipantID, int64, error) {
	decoded, err := base62.DecodeString(username)
	if err != nil {
		return "", "", 0, err
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return "", "", 0, errors.New("invalid username")
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", 0, err
	}
	if expiry == 0 {
		return "", "", 0, ErrExpired
	}

	return parts[0], livekit.ParticipantID(parts[1]), expiry, nil
}

func (h *TURNAuthHandler) CreatePassword(apiKey string, pID livekit.ParticipantID, expiry int64) (string, error) {
	if expiry == 0 || time.Now().After(time.Unix(expiry, 0)) {
		return "", ErrExpired
	}
	return h.computePassword(apiKey, pID, expiry)
}

func (h *TURNAuthHandler) computePassword(apiKey string, pID livekit.ParticipantID, expiry int64) (string, error) {
	secret := h.keyProvider.GetSecret(apiKey)
	if secret == "" {
		return "", ErrInvalidAPIKey
	}

	keyInput := fmt.Sprintf("%s|%s|%d", secret, pID, expiry)

	sum := sha256.Sum256([]byte(keyInput))
	return base62.EncodeToString(sum[:]), nil
}

func (h *TURNAuthHandler) HandleAuth(ra *turn.RequestAttributes) (userID string, key []byte, ok bool) {
	username := ra.Username
	decoded, err := base62.DecodeString(username)
	if err != nil {
		return "", nil, false
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return "", nil, false
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", nil, false
	}
	if expiry == 0 {
		return "", nil, false
	}
	expiryTime := time.Unix(expiry, 0)
	if time.Now().After(expiryTime) {
		// TTL only applies to initial allocation. Refresh / CreatePermission /
		// ChannelBind / Send / Data requests are still authenticated against the
		// username/password but skip the TTL check so long-running sessions can
		// keep refreshing past the credential expiry.
		if ra.Method == stun.MethodAllocate {
			logger.Infow("TURN credential expired", "username", decoded, "participantID", parts[1], "expiry", expiryTime, "method", ra.Method)
			return "", nil, false
		}
	}
	password, err := h.computePassword(parts[0], livekit.ParticipantID(parts[1]), expiry)
	if err != nil {
		logger.Warnw("could not create TURN password", err, "username", decoded)
		return "", nil, false
	}
	return parts[1], turn.GenerateAuthKey(username, LivekitRealm, password), true
}
