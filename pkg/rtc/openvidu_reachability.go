// BEGIN OPENVIDU BLOCK
package rtc

import (
	"context"
	"net"
	"time"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
)

const (
	validationTimeout = 5 * time.Second
)

func IsPubliclyReachable(rtcConfig *rtcconfig.RTCConfig, webrtcConfig *rtcconfig.WebRTCConfig) (bool, error) {
	nat1To1IPs := webrtcConfig.NAT1To1IPs
	if len(nat1To1IPs) != 0 {
		return true, nil
	}

	localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
	if err != nil {
		logger.Warnw("could not get local IP addresses", err)
		return false, err
	}

	nodeIP := rtcConfig.NodeIP.PrimaryIP()
	for _, localIP := range localIPs {
		if localIP == nodeIP {
			return false, nil
		}
	}

	port := int(rtcConfig.ICEPortRangeStart)
	if err := ValidateExternalIP(context.Background(), nodeIP, port); err != nil {
		logger.Warnw("external IP is not publicly reachable (UDP hairpin test failed)",
			err, "nodeIP", nodeIP)
		return false, nil
	}

	return true, nil
}

// ValidateExternalIP validates that externalIP routes back to this machine
// by performing a UDP hairpin test: listens on a local UDP port, sends a
// magic string to externalIP on that port, and checks if it arrives back.
// The port parameter specifies the UDP port to listen on (0 = OS picks).
// Use a port from the RTC port range to ensure firewall compatibility.
func ValidateExternalIP(ctx context.Context, externalIP string, port int) error {
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return err
	}
	defer srv.Close()

	magicString := "9#B8D2Nvg2xg5P$ZRwJ+f)*^Nne6*W3WamGY"

	validCh := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := srv.Read(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == magicString {
				close(validCh)
				return
			}
		}
	}()

	srvPort := srv.LocalAddr().(*net.UDPAddr).Port
	cli, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP(externalIP),
		Port: srvPort,
	})
	if err != nil {
		return err
	}
	defer cli.Close()

	if _, err = cli.Write([]byte(magicString)); err != nil {
		return err
	}

	ctx1, cancel := context.WithTimeout(ctx, validationTimeout)
	defer cancel()
	select {
	case <-validCh:
		return nil
	case <-ctx1.Done():
		return ctx1.Err()
	}
}

// END OPENVIDU BLOCK
