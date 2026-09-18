# Changelog

Notable changes of the OpenVidu fork of `livekit-server`, which is versioned in lockstep with
OpenVidu. The changes of the upstream project are listed in `CHANGELOG.md`.

## [Unreleased]

### Fixed

- **Media nodes tolerate a slow Redis**: they are no longer declared dead when Redis answers late. (OpenVidu/openvidu-livekit#13)
- **Egress and ingress listings no longer block Redis**: the hashes are scanned in chunks instead of read in one go. (OpenVidu/openvidu-livekit#13, OpenVidu/openvidu-livekit#14)
