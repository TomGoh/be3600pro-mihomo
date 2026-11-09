# BE3600 Pro resilience implementation plan

Goal: keep native Internet recoverable, improve boot recovery and observable health, then add optional usability features without changing the current routing policy.
Architecture: existing Go agent + bounded BusyBox shell + Java Android client. Emergency stop has a separate execution lane and invalidates older operations. Persistent configuration stays on flash; bounded runtime state and diagnostics stay in RAM.
Constraints: sole WAN is this router; no reboot/flash/WAN/Wi-Fi changes. Preserve the running core and node selections. No additional dependencies or secrets in source. Reserve 1 MiB flash. Physical phone acceptance waits until a phone is connected.

## Task 1: emergency stop
- [ ] RED: blocked mutation and queued enable cannot hold off emergency; cancelled old upload cannot re-enable.
- [ ] Implement independent authenticated stop, mutation generation/cancellation, shell critical-section fencing, App always-available control.
- [ ] GREEN: Go race tests, shell fake-command integration, Java host tests.

## Task 2: bootstrap recovery
- [ ] RED: failed download retries, stale/live PID lock, wrong hash and offline recovery races.
- [ ] Implement bounded retry/backoff, PID/start-time lock, official source fallback, stage/status, intent fence.
- [ ] GREEN: deterministic shell integration without router networking.

## Task 3: layered health
- [ ] RED: missing v4/v6 rules/routes, stale health and isolated UDP failure cannot falsely pass or disable all traffic.
- [ ] Implement bounded local interception verification and periodic health snapshot. External DNS/direct/proxy checks are advisory and separately reported; no whole-service action on one public target.
- [ ] GREEN: fixtures and API tests, healthy actual-router observation.

## Task 4: relay refresh
- [ ] RED: changed DNS replaces exact IP+port entries atomically; failures retain per-host last-good for at most 1h; removed hosts vanish; concurrent stop does not resurrect sets.
- [ ] Implement five-minute refresh with 10-second total DNS deadline, 8 workers, temporary ipset swap, shared lifecycle lock, bounded RAM cache.
- [ ] GREEN: resolver/clock fixtures + shell command-order fixtures; actual refresh and safe stop/re-enable smoke.

## Task 5: optional failover
- [ ] RED: manual choices preserved; only enabled small candidate groups may fail over after repeated failures and cooldown.
- [ ] Implement opt-in fallback group with bounded candidates/interval, explicit limitations for existing connections; no automatic activation in this home.
- [ ] GREEN: validation/config roundtrip and no-policy-change tests.

## Task 6: operation usability and subscription binding
- [ ] RED: job idempotency, limits/expiry, emergency cancellation, preview does not mutate, deleted selected node handled explicitly.
- [ ] Implement RAM-only bounded async subscription jobs and App polling/progress; imported snapshot binding preview and explicit apply preserve selections where names match.
- [ ] GREEN: API lifecycle tests and full Android build.

## Task 7: diagnostics and recovery
- [ ] RED: export excludes credentials, URLs/IPs/device IDs; malformed backup cannot replace config; restoring leaves proxy off.
- [ ] Implement allowlisted diagnostic export and bounded local known-good backup/manual restore, with preview and validation.
- [ ] GREEN: secret-canary and restore failure tests.

## Deployment and acceptance
- [ ] One fresh Sol 6.1 high review, address findings with regression tests.
- [ ] All CI jobs green, source secret scan, ARM build/hash/flash reserve check.
- [ ] Stage scripts and backups in router RAM; independent timed rollback before mutation; preserve core PID where possible.
- [ ] Verify emergency, local health, relay refresh and Mac direct/proxy connectivity. No actual reboot or absent-phone acceptance claim.
- [ ] Publish commits and documentation, archive sanitized evidence in chat outputs.

Review focus: child processes surviving HTTP cancellation; delayed rollback resurrecting proxy; DNS partial-family loss; interrupted PID-lock publication/PID reuse; stale success after failed async job.

Execution ledger:
- Task 1: emergency API RED (404) -> independent authenticated endpoint GREEN; cancelled mutation/old context generation regression tests pass with race detector. Shell persistent epoch uses a separate flock critical section so boot/app enable cannot overtake stop. All network/DNS preparation happens outside this lock.
- Ruling: retain inline implementation and perform one fresh Sol 6.1 high whole-branch review. The user's existing implementation/deployment authorization supersedes additional plan approval menus.

- Tasks 2–7: implementation and initial unit/host build gates completed. App 0.3.0/code5, existing signing identity and pinned recovery core preserved. Hardware phone remains absent.
- Whole-branch Sol 6.1 high review found 5 issues. Regression RED evidence recorded for TTL deadline, successful selection restore and failed restore rollback. Fixes: publish cache expiry despite DNS deadline plus kernel entry TTL; transactional state/config publication before reload; capture job generation before admission; App executor fence plus captured request epoch; transparent-socket integrity check.
- Ruling: kernel flock replaces the proposed PID/start-time reclamation algorithm. It handles exit/PID reuse without guessing process ownership; background service descriptors must be closed explicitly.
- Ruling: public UDP health uses bounded SOCKS5/STUN through mixed-port routing and remains advisory. It does not claim LAN transparent UDP or CS2 acceptance.
- Ruling: backup data stays local with credentials, while diagnostics export only nested allowlisted fields. Failed backup creation rejects a configuration transaction rather than silently claiming recoverability.
- User returned home and paused router-side testing while watching TV. **No resilience deployment has occurred.** Existing router/core/agent/config remain active. One isolated unreferenced ipset swap/2s-timeout check passed and cleaned both test sets before the pause. Continue Mac/CI only; do not deploy or run LAN/WAN load until the user resumes router testing.
- During the user's TV viewing, they reported severe buffering. With no test driver running and no new deployment, used the existing API to disable interception immediately. Confirmed both CDX_MH4/6 and CDX_DNS4/6 PREROUTING jumps absent and bridge netfilter values restored to stock 1. Kept the core standby. Awaiting user playback re-open feedback; do not re-enable automatically.

- User confirmed TV is smooth after proxy disable and playback restart. This is correlation with interception, not a CPU/UDP/DNS root-cause verdict. Keep desired-off; no automatic resume.
- Additional local regression: startup recovery must persist desired-off, not merely stop the core runtime. RED observed and fixed by explicitly calling SetEnabled(false) before recovering the journal.
