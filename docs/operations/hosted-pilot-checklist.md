# Hosted seven-day pilot checklist

Fill this form during the invitation-only Hong Kong pilot. Record aggregates, timestamps, and object names only. Do not copy viewer identifiers, session material, or configuration JSON into this file.

## Baseline

- Server plan / region / image digest:
- Domain / certificate notAfter:
- Migration head:
- Backup object / checksum / restore-drill result:
- Application key custody confirmed (yes/no, no values):
- Bilibili service credential version:
- Active invite count (at most 50 registered / 10 active):

## Security and privacy

| Check | Result | Evidence pointer |
| --- | --- | --- |
| Cross-account configuration / migration / OBS denied |  |  |
| Stale CSRF rejected |  |  |
| Reused invite rejected |  |  |
| Revoked OBS token rejected |  |  |
| Oversized or deep migration JSON rejected |  |  |
| Wrong admin TOTP rejected |  |  |
| Expired recovery code rejected |  |  |
| Public `/healthz`, `/internal/metrics`, and MySQL closed |  |  |
| Seeded privacy markers absent from dumps and six-month log samples |  |  |

## Functional and capacity

- 10 active accounts over at least 7 rooms, including 3 accounts sharing one room:
- One upstream connection per canonical room:
- Per-account state isolation:
- Room-switch barrier:
- 10-minute natural shutdown:
- Commit-before-OBS:
- Database outage degradation:
- Pending migration application:
- Rollback of the previous immutable image:

## Mainland-to-Hong-Kong and Bilibili stability

Collect China Telecom, China Unicom, and China Mobile samples in daytime and evening. Preserve raw timestamps and aggregate percentiles. Do not invent an SLA.

| Carrier | Window | DNS | TLS | Connect | First byte | Loss | OBS SSE reconnects | Bilibili uptime | Bilibili reconnects | Risk / 429 | Queue delay | CPU | RSS | MySQL p95 | Disk growth | Egress |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| China Telecom |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |
| China Unicom |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |
| China Mobile |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |

## go/no-go thresholds

Go only when every item below is true. Otherwise keep invitations capped, file a focused defect with reproduction evidence, and restart the seven-day window after the fix.

- [ ] No critical tenant, privacy, or credential issue
- [ ] Backups restored successfully
- [ ] No lost committed gameplay transitions
- [ ] p95 event commit-to-OBS under 500 ms at 10 active accounts
- [ ] Queue never exceeds 50% capacity in normal operation
- [ ] CPU p95 below 70%
- [ ] RSS p95 below 80% of limit
- [ ] Disk forecast exceeds 90 days
- [ ] Bilibili breaker and risk events are understood and recover without aggressive retries

## Decision

- Decision (go / no-go):
- Date:
- Operator:
- Notes (aggregates only):
