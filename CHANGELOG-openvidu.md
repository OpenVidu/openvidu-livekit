# Changelog

Notable changes of the OpenVidu fork of `livekit-server`, which is versioned in lockstep with
OpenVidu. The changes of the upstream project are listed in `CHANGELOG.md`.

## [Unreleased]

### Added

- **New analytics data model**: each node writes rooms, participants, tracks, subscriptions, egress, ingress, raw events, statistics and 5-minute, hourly and daily rollups to MongoDB in idempotent batches every `openvidu.analytics.interval`, with a retention per data class and a node heartbeat. Collections, validators and indexes are created or updated at startup. New `openvidu.analytics` options: `retention`, `stats_detail`, `anonymize_ip`, `store_metadata`, `events.store_types`, `queue` and `validation_action`; new metrics `openvidu_analytics_*`.
- **API calls are analytics events**: every Twirp API response emits an `API_CALL` event, counted per method in the rollups.

### Changed

- **Analytics data survives a graceful shutdown**: queued events and statistics are written, for at most 5 s, before the process exits.
- **The analytics `events` collection of previous versions is renamed to `legacy_events`** at the first start.

### Deprecated

- `openvidu.analytics.expiration`: mapped to `retention.events` and `retention.stats`, which replace it.

### Removed

- **Legacy analytics writer**: the `stats`, `active_entities` and `last_alive` collections, the legacy `events` document shape, the active-entities fixer and the Redis analytics sink.

### Fixed

- **Idle TURN connections no longer pile up**: a client that reached the TURN server and then went silent — even with just a STUN Binding — held the connection, and any allocation behind it, open indefinitely. A TURN TCP connection idle in both directions is now closed after `turn.tcp_connection_idle_timeout_seconds` (default 60s; `<= 0` disables it); an active relay is never cut.

## [3.9.0]

### Fixed

- **Media nodes tolerate a slow Redis**: they are no longer declared dead when Redis answers late. (OpenVidu/openvidu-livekit#13)
- **Egress and ingress listings no longer block Redis**: the hashes are scanned in chunks instead of read in one go. (OpenVidu/openvidu-livekit#13, OpenVidu/openvidu-livekit#14)
- **TURN-only clients keep getting relays after a network change**: the default per-participant TURN allocation quota rises from 12 to 32.
- **Paused simulcast layers stay paused after a renegotiation**: every SDP answer made the publisher resume the layers that dynacast had paused, and they stayed on. With a single PeerConnection this happened on every subscription change. The server now sends the paused state again after each burst of answers. (livekit/livekit#4907)
- **Participants that left no longer linger after a reconnect**: a client that resumed its session could keep in its list participants that had disconnected just before its connection dropped. On reconnect the server now reports the participants that disconnected since 3 s before the last signal message it got from the client, instead of only since that message. (livekit/livekit#4910)
