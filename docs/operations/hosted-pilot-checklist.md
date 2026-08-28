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
- Administrator invitation legacy-active count (`active` with NULL ciphertext; must be 0):
- Test invitation disposition (none / retained with reason / backed up and transactionally removed):

## Security and privacy

| Check | Result | Evidence pointer |
| --- | --- | --- |
| Cross-account configuration / migration / OBS denied |  |  |
| Stale CSRF rejected |  |  |
| Reused invite rejected |  |  |
| New administrator invitation remains listed after logout and login |  |  |
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

## EXE migration release gate

The V2-capable Hosted build is a hard prerequisite for any EXE release that exports `migrationVersion: 2`. Never publish or distribute that EXE first: a V1-only Hosted deployment rejects every V2 package.

- [ ] Record the exact reviewed Hosted commit and deploy it before the EXE release
- [ ] Render the production Nginx configuration and pass `nginx -t` on the deployment host
- [ ] Build the Hosted image from that commit and record its immutable digest
- [ ] Upload the checked-in `online-migration-v2-appearance.json` contract fixture and verify preview succeeds without ignored safe appearance fields
- [ ] Apply the fixture to a disposable pilot account, confirm appearance and OBS outputs, then remove only that disposable account's test data
- [ ] Verify the public Hosted build/version identifies the V2 decoder before publishing or distributing the EXE
- [ ] Record the exact reviewed EXE commit only after all preceding Hosted checks pass

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
- [ ] No unrecoverable active administrator invitation remains
- [ ] Test invitation data is absent or explicitly documented
- [ ] No lost committed gameplay transitions
- [ ] p95 event commit-to-OBS under 500 ms at 10 active accounts
- [ ] Queue never exceeds 50% capacity in normal operation
- [ ] CPU p95 below 70%
- [ ] RSS p95 below 80% of limit
- [ ] Disk forecast exceeds 90 days
- [ ] Bilibili breaker and risk events are understood and recover without aggressive retries
- [ ] Hosted V2 decoder was deployed and verified before the V2-exporting EXE was released

## Decision

- Decision (go / no-go):
- Date:
- Operator:
- Notes (aggregates only):
