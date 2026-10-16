# Recorded analytics fixtures

Protocol messages as the server hands them to the analytics writer, in protojson with proto field names (an enum at
its zero value, such as `ROOM_CREATED`, is omitted).

- `events/<TYPE>_<case>.json`: one `AnalyticsEvent` per file.
- `stats/<case>.json`: one `AnalyticsStats` flush per file.

Recorded from a local server (`cmd/server` with analytics enabled), Redis, `openvidu/egress` and `openvidu/ingress`
(the services of `openvidu/docker-compose-setup`), with the `lk` CLI as client and GStreamer as RTMP source:

- room lifecycle: room created by the first join and by the API, simulcast camera publish, subscriptions, mute and
  unmute through the API, unsubscribe and resubscribe, duplicate identity, connection timeout, subscription to a
  missing track (fails after one minute), client leave and server shutdown;
- room composite and web egress to files;
- reusable RTMP ingress with two sessions, then deleted;
- API calls, including a failed one.

Curated by hand from recorded events of the same session, because a local client cannot trigger them reliably:

- `PARTICIPANT_CONNECTION_ABORTED_join_failure`: `PARTICIPANT_LEFT` shape (the server sends it instead of
  `PARTICIPANT_LEFT` when media never connected), with `JOIN_FAILURE`.
- `PARTICIPANT_RESUMED_signal_disconnected`: participant of `PARTICIPANT_ACTIVE` with
  `client_meta {node, reconnect_reason}` and no `client_info`.

Credentials and metadata in the files (TURN password, stream key, metadata, a token in the web egress URL) are
throwaway values of the recording; tests assert that none of them is stored.
