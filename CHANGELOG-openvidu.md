# Changelog

Notable changes of the OpenVidu fork of `livekit-server`, which is versioned in lockstep with
OpenVidu. The changes of the upstream project are listed in `CHANGELOG.md`.

## [Unreleased]

### Fixed

- **Idle TURN connections no longer pile up**: a client that reached the embedded TURN server over TCP/TLS and then sent nothing — even with only an unauthenticated STUN Binding — used to hold the connection, and any allocation behind it, open until it closed on its own, because the TURN server reads each connection with no deadline. The server now closes a TURN TCP connection that is idle in both directions, after `turn.tcp_connection_idle_timeout_seconds` (default 60s; a value <= 0 disables it). An active relay keeps sending media, allocation refreshes and ICE consent checks well within the window.
- **Media nodes tolerate a slow Redis**: they are no longer declared dead when Redis answers late. (OpenVidu/openvidu-livekit#13)
- **Egress and ingress listings no longer block Redis**: the hashes are scanned in chunks instead of read in one go. (OpenVidu/openvidu-livekit#13, OpenVidu/openvidu-livekit#14)
- **TURN-only clients keep getting relays after a network change**: the default per-participant TURN allocation quota rises from 12 to 32.
- **Paused simulcast layers stay paused after a renegotiation**: every SDP answer made the publisher resume the layers that dynacast had paused, and they stayed on. With a single PeerConnection this happened on every subscription change. The server now sends the paused state again after each burst of answers. (livekit/livekit#4907)
- **Participants that left no longer linger after a reconnect**: a client that resumed its session could keep in its list participants that had disconnected just before its connection dropped. On reconnect the server now reports the participants that disconnected since 3 s before the last signal message it got from the client, instead of only since that message. (livekit/livekit#4910)
