# Hosted Administrator Redesign Plan Index

**Spec:** `docs/superpowers/specs/2026-08-22-hosted-admin-system-redesign.md`

The redesign is split into four independently reviewable plans. Execute them in order because later plans consume interfaces introduced earlier.

1. `2026-08-22-hosted-admin-auth-shell.md` — 30-day email login, operation-scoped TOTP primitive, five-item shell, shared feedback components.
2. `2026-08-22-hosted-admin-accounts-overview.md` — overview aggregation, account list/detail/bulk operations, OBS ownership inside account detail.
3. `2026-08-22-hosted-admin-invitations.md` — recoverable active eight-character codes, lifecycle, sorting/filtering, compact creation.
4. `2026-08-22-hosted-admin-service-settings-polish.md` — Bilibili service account, settings, recovery, audit/diagnostics, responsive and production verification.

Each plan must end green and committed before the next begins. Do not push, merge, deploy, tag, or modify mainland update delivery while executing these plans.

