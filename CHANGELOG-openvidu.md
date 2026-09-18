# Changelog

Notable changes of the OpenVidu fork of `livekit-server`, which is versioned in lockstep with
OpenVidu. The changes of the upstream project are listed in `CHANGELOG.md`.

## [Unreleased]

### Fixed

- **Healthy media nodes are no longer declared dead when Redis answers slowly.** A media node
  refreshes its registration with a keepalive ping that travels through Redis. When Redis was slow
  the ping arrived late and was dropped, the node stats went stale, and after 5 seconds the other
  nodes removed the node from the cluster and tore down its rooms. The keepalive is now dropped
  only when the node's own timer fired late (a slow Redis just delays it), a node is removed after
  30 seconds without stats instead of 5, and in Sentinel mode the Redis client timeouts default to
  the go-redis values (dial 5 s, read and write 3 s) instead of the 200 ms of `livekit/protocol`.
  The analytics fixer also pauses 5 s instead of spinning when Redis itself fails to grant its
  lock. (OpenVidu/openvidu-livekit#13)

- **Listing egresses and ingresses, and cleaning up ended egresses, no longer block Redis.** The
  `egress`, `ended_egress` and `ingress` hashes were read whole with `HGETALL`. With hundreds of
  thousands of entries one call kept the Redis main thread busy for hundreds of milliseconds, and
  the ended-egress cleanup fired that call from every media node at the same minute. The hashes
  are now walked with `HSCAN` in chunks of 500, the ingress states of each chunk are read in one
  pipeline, and the cleanup runs on a single node per cycle, starts at a random point of the
  interval, keeps going past corrupt entries and reports what it scanned and deleted.
  (OpenVidu/openvidu-livekit#13, OpenVidu/openvidu-livekit#14)
