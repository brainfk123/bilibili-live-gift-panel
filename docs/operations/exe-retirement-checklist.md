# EXE 退役证据清单

本清单是 Hosted 迁移的生产门槛记录，不是日期计划。所有复选框必须由可复核的产物、日志、截图清单或签字决策支持；日期 alone never satisfy entry。`CI success is not production acceptance`：CI 通过不能替代真实 Windows、真实 Bilibili 连接或用户迁移验收。当前所有门槛均未满足，故不勾选任何项目。

权威入口：

- [Hosted pilot checklist](hosted-pilot-checklist.md)
- [Mac/Hosted/Windows 兼容性设计](../superpowers/specs/2026-08-31-mac-hosted-windows-compatibility-design.md)
- [Hosted 在线服务设计](../superpowers/specs/2026-08-16-hosted-online-service-design.md)
- [Hosted 配置迁移计划](../superpowers/plans/2026-08-16-hosted-configuration-migration.md)
- [Hosted 运维试点计划](../superpowers/plans/2026-08-16-hosted-operations-pilot.md)

预检裁决：原 brief 提及的三个 `2026-08-25` 计划文件是主工作树用户拥有的未跟踪文件，本分支不复制、不提交，也不生成指向它们的坏链接。其证据入口统一以上述已跟踪文档为准。Gameplay migration、media parity 和完整 EXE-versus-Hosted UI parity 仍是未满足门槛。

## Stage A: migration-development

### Entry evidence

- [ ] Core parity evidence pointer:
- [ ] media parity evidence pointer:
- [ ] EXE-versus-Hosted screenshots manifest and SHA-256:
- [ ] migration export, preview, apply, and seven-day rollback evidence:
- [ ] Hong Kong backup restore evidence:
- [ ] real Bilibili connections and seven-day pilot decision:

### Windows policy after entry

- Shared core, migration, update security, and critical EXE defects only.
- No new EXE product capabilities.

### Return to previous stage when

- Any declared migration content is lost, any parity state is missing, rollback fails, or pilot go/no-go returns no-go.

Stage A must prove the full migration lifecycle, including export, preview, apply, and seven-day rollback. TypeScript exporter v2 and Hosted decoder compatibility are currently unproven; they must be demonstrated against the gameplay migration evidence before Stage B.

## Stage B: closed-pilot-and-voluntary-migration

### Entry evidence

- [ ] Core parity evidence pointer:
- [ ] media parity evidence pointer:
- [ ] EXE-versus-Hosted screenshots manifest and SHA-256:
- [ ] migration export, preview, apply, and seven-day rollback evidence:
- [ ] Hong Kong backup restore evidence:
- [ ] real Bilibili connections and seven-day pilot decision:

### Windows policy after entry

- Shared core, migration, update security, and critical EXE defects only.
- No new EXE product capabilities.

### Return to previous stage when

- Any declared migration content is lost, any parity state is missing, rollback fails, or pilot go/no-go returns no-go.

Stage B requires closed-pilot evidence from real Bilibili connections and voluntary user migration. No entry is allowed while media parity, complete UI parity, or TypeScript exporter v2 / Hosted decoder compatibility lacks production evidence.

## Stage C: exe-feature-freeze

### Entry evidence

- [ ] Core parity evidence pointer:
- [ ] media parity evidence pointer:
- [ ] EXE-versus-Hosted screenshots manifest and SHA-256:
- [ ] migration export, preview, apply, and seven-day rollback evidence:
- [ ] Hong Kong backup restore evidence:
- [ ] real Bilibili connections and seven-day pilot decision:
- [ ] user notification, support owner, and support policy:

### Windows policy after entry

- Shared core, migration, update security, and critical EXE defects only.
- No new EXE product capabilities; support is limited to the documented policy.

### Return to previous stage when

- Any declared migration content is lost, any parity state is missing, rollback fails, pilot go/no-go returns no-go, or user support coverage is insufficient.

Stage C may begin only after migration and parity evidence is complete and users have been notified with a supported return path.

## Stage D: maintenance-ended

### Entry evidence

- [ ] Core parity evidence pointer:
- [ ] media parity evidence pointer:
- [ ] EXE-versus-Hosted screenshots manifest and SHA-256:
- [ ] migration export, preview, apply, and seven-day rollback evidence:
- [ ] Hong Kong backup restore evidence:
- [ ] real Bilibili connections and seven-day pilot decision:
- [ ] user notification, support owner, and support policy:
- [ ] archived signed EXE, SHA-256, signer subject, and source commit:
- [ ] reproducible build instructions and migration instructions:
- [ ] explicit old-package distribution, update, and rollback policy (old migration-package policy):

### Windows policy after entry

- Shared core, migration, and update-security fixes only; no EXE product development or feature commitments.
- Retain the archived signed EXE and its provenance for the stated support/rollback period.

### Return to previous stage when

- Any declared migration content is lost, any parity state is missing, rollback fails, pilot go/no-go returns no-go, support obligations cannot be met, or archived provenance/package policy is unavailable.

Maintenance can end only with explicit old-package policy and reproducible provenance. A date, a green CI run, or a successful build alone never satisfies this stage.
