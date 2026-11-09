# BE3600 Pro resilience implementation plan

Goal: keep native Internet recoverable, improve boot recovery and observable health, then add optional usability features without changing the current routing policy.
Architecture: existing Go agent + bounded BusyBox shell + Java Android client. Emergency stop has a separate execution lane and invalidates older operations. Persistent configuration stays on flash; bounded runtime state and diagnostics stay in RAM.
Constraints: sole WAN is this router; no reboot/flash/WAN/Wi-Fi changes. Preserve the running core and node selections. No additional dependencies or secrets in source. Reserve 1 MiB flash. Physical phone acceptance waits until a phone is connected.

## Task 1: emergency stop
- [ ] RED: blocked mutation and queued enable cannot hold off emergency; cancelled old upload cannot re-enable.
- [x] Implement independent authenticated stop, mutation generation/cancellation, shell critical-section fencing, App always-available control.
- [ ] GREEN: Go race tests, shell fake-command integration, Java host tests.

## Task 2: bootstrap recovery
- [ ] RED: failed download retries, stale/live PID lock, wrong hash and offline recovery races.
- [x] Implement bounded retry/backoff, PID/start-time lock, official source fallback, stage/status, intent fence.
- [ ] GREEN: deterministic shell integration without router networking.

## Task 3: layered health
- [ ] RED: missing v4/v6 rules/routes, stale health and isolated UDP failure cannot falsely pass or disable all traffic.
- [x] Implement bounded local interception verification and periodic health snapshot. External DNS/direct/proxy checks are advisory and separately reported; no whole-service action on one public target.
- [ ] GREEN: fixtures and API tests, healthy actual-router observation.

## Task 4: relay refresh
- [ ] RED: changed DNS replaces exact IP+port entries atomically; failures retain per-host last-good for at most 1h; removed hosts vanish; concurrent stop does not resurrect sets.
- [x] Implement five-minute refresh with 10-second total DNS deadline, 8 workers, temporary ipset swap, shared lifecycle lock, bounded RAM cache.
- [ ] GREEN: resolver/clock fixtures + shell command-order fixtures; actual refresh and safe stop/re-enable smoke.

## Task 5: optional failover
- [ ] RED: manual choices preserved; only enabled small candidate groups may fail over after repeated failures and cooldown.
- [x] Implement opt-in fallback group with bounded candidates/interval, explicit limitations for existing connections; no automatic activation in this home.
- [ ] GREEN: validation/config roundtrip and no-policy-change tests.

## Task 6: operation usability and subscription binding
- [ ] RED: job idempotency, limits/expiry, emergency cancellation, preview does not mutate, deleted selected node handled explicitly.
- [x] Implement RAM-only bounded async subscription jobs and App polling/progress; imported snapshot binding preview and explicit apply preserve selections where names match.
- [ ] GREEN: API lifecycle tests and full Android build.

## Task 7: diagnostics and recovery
- [ ] RED: export excludes credentials, URLs/IPs/device IDs; malformed backup cannot replace config; restoring leaves proxy off.
- [x] Implement allowlisted diagnostic export and bounded local known-good backup/manual restore, with preview and validation.
- [ ] GREEN: secret-canary and restore failure tests.

## Deployment and acceptance
- [ ] One fresh Sol 6.1 high review, address findings with regression tests.
- [ ] All CI jobs green, source secret scan, ARM build/hash/flash reserve check.
- [ ] Stage scripts and backups in router RAM; independent timed rollback before mutation; preserve core PID where possible.
- [ ] Verify emergency, local health, relay refresh and Mac direct/proxy connectivity. No actual reboot or absent-phone acceptance claim.
- [ ] Publish commits and documentation, archive sanitized evidence in chat outputs.

Review focus: child processes surviving HTTP cancellation; delayed rollback resurrecting proxy; DNS partial-family loss; interrupted PID-lock publication/PID reuse; stale success after failed async job.

Execution status (source checkpoint):
- All seven implementation areas are present. New features remain preview code pending hardware integration acceptance.
- One whole-branch Sol 6.1 high review and a focused fix verification completed. Five findings addressed: stale Android queued work, relay expiry on DNS deadline, matching restore/rollback selections, job admission intent, and transparent-socket health coverage.
- Local gates: 35 existing tool/firewall tests, 10 resilience shell tests, 43 evidence/CLI tests with loopback integrations, Go race/vet and ARMv7 build, Android host tests and complete signed APK.
- Nine isolated negative controls exercise actual assertion failures for the cancellation fences, remaining relay TTL, matching config/state publication, UDP transaction validation, health rule coverage, shell intent and Android queue fencing. Mutations were restored and never committed.
- Implementation CI: https://github.com/TomGoh/be3600pro-mihomo/actions/runs/42599620783 — five jobs passed. Supplemental test-only changes are verified separately.
- Kernel flock replaces PID/start-time lock reclamation: process exit releases ownership without PID-reuse guesses. Service/Dropbear launch descriptors are closed explicitly.
- Public UDP probes are advisory SOCKS5/STUN checks, not LAN TPROXY or game acceptance. Backup credentials stay local; diagnostics use nested allowlists.
- Production deployment, real cold reboot, combined bootstrap/offline-upload recovery, live relay migration and Android phone HTTP/UI acceptance remain pending. No automatic deployment is performed by CI.
