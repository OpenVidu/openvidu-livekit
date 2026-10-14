# Recorded legacy analytics documents

Collections of the legacy analytics writer (before 3.10), in canonical Extended JSON with one document per line, as
`mongoexport` writes them:

- `legacy/events.json`, `legacy/stats.json`, `legacy/last_alive.json`, `legacy/active_entities.json`.

Recorded from the community local deployment with the legacy server, running the scenarios of the OpenVidu
Dashboard's active-entities study (`tools/research/active-entities`, scenarios 00 to 08, rooms `ov-study-*`): every
event type the legacy writer stored, including `WEBHOOK`, six close events inserted by the active-entities fixer
(ObjectId `_id`, no `id`), egress requests under Go-named oneof wrappers, enums stored as names and as numbers, and
nanosecond timestamps as doubles. TURN passwords were replaced by `SECRET-turn` on export.

`TestExportLegacyFixtures` re-records the files from a legacy database (`MIGRATION_LEGACY_SOURCE_URI` and
`MIGRATION_LEGACY_SOURCE_DB`). Event types, egress variants and fixer close events the recording lacks are produced in
the tests with the legacy encoding (`legacy_test.go`, and `legacyFixerClose` in the writer's migration tests).
