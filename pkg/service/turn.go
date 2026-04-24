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
	"github.com/pion/turn/v4"
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
	turnMinPort     = 1024
	turnMaxPort     = 30000

	// BEGIN OPENVIDU BLOCK
	// turnExpirySkew absorbs clock drift between the node that issued the credential
	// (during JoinResponse) and the node that authenticates the TURN allocation.
	// Also covers brief NTP outages and VM pause/resume gaps.
	// 5 minutes sits inside the "usually no more than a few minutes" guidance from
	// RFC 7519 / OpenID Connect Core, and matches the long-standing MIT Kerberos
	// clockskew default (300s). Negligible fraction of the 24h default TTL.
	turnExpirySkew = 5 * time.Minute
	// END OPENVIDU BLOCK
)

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
	// END OPENVIDU BLOCK

	if !turnConf.Enabled {
		return nil, nil
	}

	if turnConf.TLSPort <= 0 && turnConf.UDPPort <= 0 {
		return nil, errors.New("invalid TURN ports")
	}

	serverConfig := turn.ServerConfig{
		Realm:         LivekitRealm,
		AuthHandler:   authHandler,
		LoggerFactory: pionlogger.NewLoggerFactory(logger.GetLogger()),
	}

	var relayAddrGen turn.RelayAddressGenerator = &turn.RelayAddressGeneratorPortRange{
		// BEGIN OPENVIDU BLOCK
		RelayAddress: net.ParseIP(relayAddress),
		// END OPENVIDU BLOCK
		Address:    "0.0.0.0",
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
	)
	if conf.RTC.ICEPortRangeStart != 0 && conf.RTC.ICEPortRangeEnd != 0 {
		logger.Infow("TURN relay peer port restriction enabled",
			"minPort", conf.RTC.ICEPortRangeStart,
			"maxPort", conf.RTC.ICEPortRangeEnd,
		)
	} else {
		logger.Warnw("TURN relay peer port restriction: no ICE port range configured, all peer relay ports will be denied", nil)
	}
	permissionHandler := NewTURNSecurity(conf, rc).PermissionHandler()
	// END OPENVIDU BLOCK

	var logValues []any

	logValues = append(logValues, "turn.relay_range_start", turnConf.RelayPortRangeStart)
	logValues = append(logValues, "turn.relay_range_end", turnConf.RelayPortRangeEnd)

	if turnConf.TLSPort > 0 {
		if turnConf.Domain == "" {
			return nil, errors.New("TURN domain required")
		}

		if !IsValidDomain(turnConf.Domain) {
			return nil, errors.New("TURN domain is not correct")
		}

		if !turnConf.ExternalTLS {
			cert, err := tls.LoadX509KeyPair(turnConf.CertFile, turnConf.KeyFile)
			if err != nil {
				return nil, errors.Wrap(err, "TURN tls cert required")
			}

			tlsListener, err := tls.Listen("tcp4", "0.0.0.0:"+strconv.Itoa(turnConf.TLSPort),
				&tls.Config{
					MinVersion:   tls.VersionTLS12,
					Certificates: []tls.Certificate{cert},
				})
			if err != nil {
				return nil, errors.Wrap(err, "could not listen on TURN TCP port")
			}
			if standalone {
				tlsListener = telemetry.NewListener(tlsListener)
			}

			listenerConfig := turn.ListenerConfig{
				Listener:              tlsListener,
				RelayAddressGenerator: relayAddrGen,
				PermissionHandler:     permissionHandler, // OPENVIDU
			}
			serverConfig.ListenerConfigs = append(serverConfig.ListenerConfigs, listenerConfig)
		} else {
			tcpListener, err := net.Listen("tcp4", "0.0.0.0:"+strconv.Itoa(turnConf.TLSPort))
			if err != nil {
				return nil, errors.Wrap(err, "could not listen on TURN TCP port")
			}
			if standalone {
				tcpListener = telemetry.NewListener(tcpListener)
			}

			listenerConfig := turn.ListenerConfig{
				Listener:              tcpListener,
				RelayAddressGenerator: relayAddrGen,
				PermissionHandler:     permissionHandler, // OPENVIDU
			}
			serverConfig.ListenerConfigs = append(serverConfig.ListenerConfigs, listenerConfig)
		}
		logValues = append(logValues, "turn.portTLS", turnConf.TLSPort, "turn.externalTLS", turnConf.ExternalTLS)
	}

	if turnConf.UDPPort > 0 {
		udpListener, err := net.ListenPacket("udp4", "0.0.0.0:"+strconv.Itoa(turnConf.UDPPort))
		if err != nil {
			return nil, errors.Wrap(err, "could not listen on TURN UDP port")
		}

		if standalone {
			udpListener = telemetry.NewPacketConn(udpListener, prometheus.Incoming)
		}

		packetConfig := turn.PacketConnConfig{
			PacketConn:            udpListener,
			RelayAddressGenerator: relayAddrGen,
			PermissionHandler:     permissionHandler, // OPENVIDU
		}
		serverConfig.PacketConnConfigs = append(serverConfig.PacketConnConfigs, packetConfig)
		logValues = append(logValues, "turn.portUDP", turnConf.UDPPort)
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
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, preferredInterfaces)
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
	return conf.RTC.NodeIP, nil
}

// END OPENVIDU BLOCK

func getTURNAuthHandlerFunc(handler *TURNAuthHandler) turn.AuthHandler {
	return handler.HandleAuth
}

type TURNAuthHandler struct {
	keyProvider auth.KeyProvider
	// BEGIN OPENVIDU BLOCK
	// now is injectable so tests can drive the expiry clock without time.Sleep.
	now func() time.Time
	// END OPENVIDU BLOCK
}

func NewTURNAuthHandler(keyProvider auth.KeyProvider) *TURNAuthHandler {
	return &TURNAuthHandler{
		keyProvider: keyProvider,
		// BEGIN OPENVIDU BLOCK
		now: time.Now,
		// END OPENVIDU BLOCK
	}
}

// BEGIN OPENVIDU BLOCK — Credential format with expiry binding.
//
// Username plaintext (before base62 encoding):
//
//	apiKey|pID               (legacy, no expiry)
//	apiKey|pID|<unixSeconds> (with expiry)
//
// Password plaintext (before SHA256+base62):
//
//	secret|pID               (legacy, no expiry)
//	secret|pID|<unixSeconds> (with expiry)
//
// The expiry is bound into BOTH the username and the password hash. This
// matters for the TTL to be enforceable: the TURN long-term-credential auth
// key is MD5(username:realm:password), so without expiry binding, a leaked
// 3-part credential could be re-encoded as a 2-part username (stripping the
// expiry) and the server-side password would be unchanged — the auth key
// would match and MESSAGE-INTEGRITY would pass. Binding the expiry into the
// password makes the stripped-username form compute a different password
// server-side, so any captured password no longer produces a matching key.

// CreateCredentials generates a matched (username, password) pair. The expiry
// is computed once from ttl and bound into both sides, so the pair cannot be
// re-encoded into a different username form without invalidating the password.
// ttl<=0 emits the legacy no-expiry forms (opt-out via config).
func (h *TURNAuthHandler) CreateCredentials(apiKey string, pID livekit.ParticipantID, ttl time.Duration) (username string, password string, err error) {
	if err := validateCredentialInputs(apiKey, pID); err != nil {
		logger.Errorw("TURN credential creation rejected: invalid input", err, "apiKey", apiKey, "pID", pID)
		return "", "", err
	}
	expiry := h.expiryFor(ttl)
	password, err = h.CreatePassword(apiKey, pID, expiry)
	if err != nil {
		logger.Debugw("TURN credential creation failed: password derivation", "err", err, "apiKey", apiKey, "pID", pID)
		return "", "", err
	}
	username = h.CreateUsername(apiKey, pID, expiry)
	logger.Debugw("TURN credentials created",
		"apiKey", apiKey,
		"pID", pID,
		"ttl", ttl,
		"expiry", expiry,
		"username", username,
		"password", password,
	)
	return username, password, nil
}

// validateCredentialInputs rejects apiKey/pID values containing the '|' field
// separator or NUL bytes. Both would produce a username that ParseUsername
// cannot round-trip, causing every allocation to fail authentication silently.
// apiKey is operator-configured and pID is server-generated, so a non-empty
// result here indicates misconfiguration or a bug — fail loud, not silent.
func validateCredentialInputs(apiKey string, pID livekit.ParticipantID) error {
	if strings.ContainsAny(apiKey, "|\x00") {
		return errors.Wrap(ErrInvalidCredentialInput, "apiKey")
	}
	if strings.ContainsAny(string(pID), "|\x00") {
		return errors.Wrap(ErrInvalidCredentialInput, "pID")
	}
	return nil
}

func (h *TURNAuthHandler) expiryFor(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return h.now().Add(ttl)
}

// CreateUsername encodes an apiKey/pID pair into an opaque TURN username.
// Zero-value expiry emits the legacy 2-part form.
func (h *TURNAuthHandler) CreateUsername(apiKey string, pID livekit.ParticipantID, expiry time.Time) string {
	if expiry.IsZero() {
		return base62.EncodeToString([]byte(fmt.Sprintf("%s|%s", apiKey, pID)))
	}
	return base62.EncodeToString([]byte(fmt.Sprintf("%s|%s|%d", apiKey, pID, expiry.Unix())))
}

// ParseUsername decodes the username and returns the embedded fields.
// A zero-value expiry means the credential has no expiry (legacy 2-part form).
func (h *TURNAuthHandler) ParseUsername(username string) (apiKey string, pID livekit.ParticipantID, expiry time.Time, err error) {
	decoded, err := base62.DecodeString(username)
	if err != nil {
		return "", "", time.Time{}, err
	}
	parts := strings.Split(string(decoded), "|")
	switch len(parts) {
	case 2:
		return parts[0], livekit.ParticipantID(parts[1]), time.Time{}, nil
	case 3:
		sec, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return "", "", time.Time{}, errors.Wrap(err, "invalid username expiry")
		}
		return parts[0], livekit.ParticipantID(parts[1]), time.Unix(sec, 0), nil
	default:
		return "", "", time.Time{}, errors.New("invalid username")
	}
}

// END OPENVIDU BLOCK

// CreatePassword derives the TURN long-term credential password.
// Zero-value expiry emits the legacy hash for backward compatibility; a non-zero
// expiry binds into the hash so stripping a 3-part username down to 2-part
// produces a mismatched password server-side. See the BLOCK comment above.
func (h *TURNAuthHandler) CreatePassword(apiKey string, pID livekit.ParticipantID, expiry time.Time) (string, error) {
	secret := h.keyProvider.GetSecret(apiKey)
	if secret == "" {
		return "", ErrInvalidAPIKey
	}
	var input string
	if expiry.IsZero() {
		input = fmt.Sprintf("%s|%s", secret, pID)
	} else {
		input = fmt.Sprintf("%s|%s|%d", secret, pID, expiry.Unix())
	}
	sum := sha256.Sum256([]byte(input))
	return base62.EncodeToString(sum[:]), nil
}

func (h *TURNAuthHandler) HandleAuth(username, realm string, srcAddr net.Addr) (key []byte, ok bool) {
	// BEGIN OPENVIDU BLOCK — parse once via ParseUsername, validate expiry,
	// and derive the password with the SAME expiry that's in the username.
	apiKey, pID, expiry, err := h.ParseUsername(username)
	if err != nil {
		logger.Infow("TURN auth rejected: invalid username", "err", err, "username", username, "realm", realm, "srcAddr", srcAddr)
		return nil, false
	}
	if !expiry.IsZero() {
		now := h.now()
		if now.After(expiry.Add(turnExpirySkew)) {
			logger.Infow("TURN auth rejected: credential expired",
				"apiKey", apiKey,
				"pID", pID,
				"expiredAgo", now.Sub(expiry),
				"expiry", expiry,
				"srcAddr", srcAddr,
			)
			return nil, false
		}
	}
	password, err := h.CreatePassword(apiKey, pID, expiry)
	if err != nil {
		logger.Warnw("could not create TURN password", err, "username", username, "apiKey", apiKey, "pID", pID, "srcAddr", srcAddr)
		return nil, false
	}
	logger.Debugw("TURN auth succeeded",
		"apiKey", apiKey,
		"pID", pID,
		"expiry", expiry,
		"username", username,
		"password", password,
		"realm", realm,
		"srcAddr", srcAddr,
	)
	return turn.GenerateAuthKey(username, LivekitRealm, password), true
	// END OPENVIDU BLOCK
}
