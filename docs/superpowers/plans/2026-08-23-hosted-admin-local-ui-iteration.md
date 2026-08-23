# Hosted Administrator Local UI Iteration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a local production-component simulator and fix administrator controls, account layout, invitation state handling, Bilibili service feedback, and QR layout before any production deployment.

**Architecture:** Production views remain small DOM modules under `src/hosted/admin`. Shared UI state is implemented by focused `notice` and `async-action` modules; page models own business state and never encode data in transient DOM text. A local-only Vite entry in `.cache/admin-preview/` mounts the real views against an in-memory API and is never staged or included in the Hosted Vite inputs.

**Tech Stack:** TypeScript, DOM APIs, Vite 5, Vitest 2, CSS, Playwright-compatible in-app browser checks.

**Spec:** `docs/superpowers/specs/2026-08-23-hosted-admin-local-iteration-design.md`

## Global Constraints

- Do not connect the simulator to production, send email, read production Cookie, or mutate real accounts.
- Keep `.cache/admin-preview/`, screenshots, and scenario data untracked.
- Every product behavior starts with a failing Vitest test and is committed independently.
- Native button, select, checkbox, details, and dialog semantics remain keyboard accessible.
- Motion is 120–180ms and disabled where required by `prefers-reduced-motion: reduce`.
- A failed refresh preserves the last successful data set.
- Do not deploy, merge master, or modify the Shanghai update mirror, COS paths, or release system.

---

### Task 1: Shared administrator interaction primitives

**Files:**
- Create: `src/hosted/admin/ui/notice.ts`
- Create: `src/hosted/admin/ui/async-action.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-interactions.test.ts`

**Interfaces:**
- Produces: `mountAdminNotice(host: HTMLElement): AdminNoticeControl`
- Produces: `runAdminAction(button: HTMLButtonElement, labels: AdminActionLabels, operation: () => Promise<void>): Promise<'success' | 'failure'>`
- `AdminNoticeControl.show(kind, message, retry?)` renders `info | success | warning | error`; `clear()` hides without mutating page data.

- [ ] **Step 1: Write failing DOM tests for notice lifetime and button states**

```ts
it('closes a success notice without removing neighboring business content', () => {
  const host = document.createElement('section');
  const table = document.createElement('div'); table.dataset.testid = 'inventory';
  host.append(table);
  const notice = mountAdminNotice(host);
  notice.show('success', '已创建 1 个邀请码');
  notice.element.querySelector<HTMLButtonElement>('button')!.click();
  expect(host.querySelector('[data-testid="inventory"]')).toBe(table);
  expect(notice.element.hidden).toBe(true);
});

it('keeps a button busy until its operation settles', async () => {
  const button = document.createElement('button'); button.textContent = '立即检查';
  let finish!: () => void;
  const pending = new Promise<void>((resolve) => { finish = resolve; });
  const action = runAdminAction(button, { idle: '立即检查', busy: '检查中…' }, () => pending);
  expect(button.disabled).toBe(true);
  expect(button.getAttribute('aria-busy')).toBe('true');
  expect(button.textContent).toContain('检查中');
  finish(); await action;
  expect(button.disabled).toBe(false);
  expect(button.getAttribute('aria-busy')).toBeNull();
});
```

- [ ] **Step 2: Run the new test and verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-interactions.test.ts`

Expected: FAIL because `notice.ts` and `async-action.ts` do not exist.

- [ ] **Step 3: Implement the minimal notice and async-action modules**

```ts
export type AdminNoticeKind = 'info'|'success'|'warning'|'error';
export interface AdminNoticeControl {
  readonly element: HTMLElement;
  show(kind: AdminNoticeKind, message: string, retry?: () => void): void;
  clear(): void;
  dispose(): void;
}

export interface AdminActionLabels { idle: string; busy: string; }
export async function runAdminAction(
  button: HTMLButtonElement,
  labels: AdminActionLabels,
  operation: () => Promise<void>,
): Promise<'success'|'failure'>;
```

The notice owns only its own element. `runAdminAction` sets `disabled`, `aria-busy`, a `.hosted-admin-action-spinner`, and restores the idle label in `finally`; it never catches away the operation error before returning `failure`.

- [ ] **Step 4: Add shared CSS contracts**

Style `.hosted-admin-content button`, `[data-variant]`, `.hosted-admin-notice`, `.hosted-admin-action-spinner`, `.hosted-admin-content select`, and `.hosted-admin-content input[type=checkbox]`. Add hover, active, focus-visible, disabled, busy, and reduced-motion rules. Use `appearance:none` plus an inline SVG background arrow for native select.

- [ ] **Step 5: Run focused and existing admin tests**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-interactions.test.ts tests/hosted-admin-shell.test.ts tests/hosted-admin-view.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/hosted/admin/ui/notice.ts src/hosted/admin/ui/async-action.ts src/hosted/shell.css tests/hosted-admin-interactions.test.ts
git commit -m "feat(hosted): add administrator interaction feedback"
```

### Task 2: Overview and account workspace layout

**Files:**
- Modify: `src/hosted/admin/overview.ts`
- Modify: `src/hosted/admin/accounts/list.ts`
- Modify: `src/hosted/admin/accounts/detail.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-overview.test.ts`
- Test: `tests/hosted-admin-accounts.test.ts`
- Test: `tests/hosted-admin-view.test.ts`

**Interfaces:**
- Consumes: `mountAdminNotice`, `runAdminAction`
- Produces: labeled account filters, accessible current-page selection, stable batch result feedback, and resource cards with descriptions.

- [ ] **Step 1: Add failing view tests**

```ts
it('labels both account filters and the current-page checkbox', async () => {
  const view = mountAccountList(root, api);
  await flush();
  expect(control(root, 'select', '账号状态').getAttribute('aria-label')).toBe('账号状态');
  expect(control(root, 'select', '关注事项').getAttribute('aria-label')).toBe('关注事项');
  expect(control(root, 'input', '全选当前页').getAttribute('aria-label')).toBe('全选当前页');
  await view.dispose();
});

it('renders resource destinations as descriptive cards', async () => {
  mountAdminOverview(root, api, navigate);
  await flush();
  expect(text(root)).toContain('管理直播间、邀请额度与 OBS');
  expect(text(root)).toContain('创建、分享与作废邀请码');
});
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts`

Expected: FAIL because labels, resource descriptions, and stable batch feedback are absent.

- [ ] **Step 3: Implement semantic filter and resource layouts**

Wrap each select in `.hosted-admin-field` with a visible `<span>` label. Add `aria-label` to selection checkboxes. Render overview resources as `.hosted-admin-resource-card` buttons containing a title, description, and `aria-hidden` arrow.

- [ ] **Step 4: Preserve failed batch selections and show result notice**

After `adminBatch`, remove only succeeded IDs from selection; failed IDs remain selected. Show `success` when all succeed and `warning` with exact counts when some fail. On request rejection show `error` with retry bound to the same action and reason.

- [ ] **Step 5: Verify focused tests and desktop/mobile CSS**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/hosted/admin/overview.ts src/hosted/admin/accounts/list.ts src/hosted/admin/accounts/detail.ts src/hosted/shell.css tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts
git commit -m "feat(hosted): polish administrator resource workspaces"
```

### Task 3: Invitation inventory state isolation

**Files:**
- Create: `src/hosted/admin/invitations/controller.ts`
- Modify: `src/hosted/admin/invitations/view.ts`
- Modify: `src/hosted/admin/invitations/create-panel.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-invitation-view.test.ts`
- Test: `tests/hosted-admin-invitations.test.ts`

**Interfaces:**
- Produces: `createInvitationInventoryController(api, render): InvitationInventoryController`
- Controller snapshot: `{ rows, loading, creating, error?, notice? }`
- `reload()` preserves `rows` on failure; `copy(id)` and `share(id)` never call `adminInvitations`; `revoke(id)` updates only the target row after success.

- [ ] **Step 1: Write the failing regression tests**

```ts
it('preserves the last successful rows when a refresh fails', async () => {
  api.adminInvitations.mockResolvedValueOnce({ invitations: [active] });
  const controller = createInvitationInventoryController(api, render);
  await controller.reload();
  api.adminInvitations.mockRejectedValueOnce(new Error('offline'));
  await controller.reload();
  expect(last(render).rows).toEqual([active]);
  expect(last(render).error).toBe('邀请码列表加载失败，请重试');
});

it('does not reload or clear rows when copy and share are used', async () => {
  const controller = createInvitationInventoryController(api, render, clipboard, share);
  await controller.reload();
  await controller.copy(active.id);
  await controller.share(active.id);
  expect(api.adminInvitations).toHaveBeenCalledTimes(1);
  expect(last(render).rows).toEqual([active]);
});
```

- [ ] **Step 2: Run the regression test and verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-invitation-view.test.ts`

Expected: FAIL because the controller does not exist.

- [ ] **Step 3: Implement the controller with generation fencing**

The controller owns cloned rows and a generation counter. `reload` publishes loading without replacing rows. A stale response cannot publish. `create` merges returned rows by ID. `revoke` replaces the matching row with `status:'revoked'` and removes its full `code`; a follow-up reload failure leaves that local state visible.

- [ ] **Step 4: Rebuild the invitation view around controller snapshots**

Use a labeled status select, a notice host, a formal empty state, a table host, and a collapsible create card. Action buttons use `secondary`, `quiet`, or `danger`. Closing the notice calls only `notice.clear()`.

- [ ] **Step 5: Add clipboard/share degradation**

Treat `AbortError` from `navigator.share` as cancellation. For clipboard failure, show an error notice and render the code in a selectable `<code>` element with the instruction “长按或拖选后复制”.

- [ ] **Step 6: Run invitation and API tests**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-invitation-view.test.ts tests/hosted-admin-invitations.test.ts tests/hosted-auth.test.ts`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/hosted/admin/invitations/controller.ts src/hosted/admin/invitations/view.ts src/hosted/admin/invitations/create-panel.ts src/hosted/shell.css tests/hosted-admin-invitation-view.test.ts tests/hosted-admin-invitations.test.ts
git commit -m "fix(hosted): preserve administrator invitation inventory"
```

### Task 4: Bilibili service action and QR workflow

**Files:**
- Create: `src/hosted/admin/bili-service-controller.ts`
- Modify: `src/hosted/admin/bili-service.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-bili-service-view.test.ts`
- Test: `tests/hosted-bili-service-admin.test.ts`

**Interfaces:**
- Produces: `createBiliServiceController(api, render): BiliServiceController`
- State: `phase: 'idle'|'checking'|'qr'|'authorizing'|'replacing'`, `status?`, `challenge?`, `notice?`
- Commands: `load`, `check`, `beginReplacement`, `cancelReplacement`, `authorizeAndReplace`.

- [ ] **Step 1: Write failing state tests**

```ts
it('publishes checking and a visible success result', async () => {
  const controller = createBiliServiceController(api, render);
  await controller.check();
  expect(render.mock.calls.map(([state]) => state.phase)).toContain('checking');
  expect(last(render).notice).toEqual({ kind:'success', message:'检查完成，服务账号运行正常' });
});

it('keeps the continue button below a bounded QR image', async () => {
  mountBiliServiceView(root, api);
  await click(root, '更换服务账号');
  const flow = root.querySelector('.hosted-admin-bili-flow')!;
  expect([...flow.children].map((node) => node.className)).toEqual([
    'hosted-admin-bili-step', 'hosted-admin-bili-qr', 'hosted-admin-bili-flow-actions'
  ]);
});
```

- [ ] **Step 2: Run tests and verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-bili-service-view.test.ts`

Expected: FAIL because no controller or structured flow exists.

- [ ] **Step 3: Implement the controller**

Fence async operations by generation. `check` publishes `checking`, then success/error notice and the returned status. `beginReplacement` stores `challengeId`, `qrImage`, and `expiresAt`. `cancelReplacement` erases all challenge values. A successful replacement clears the workflow and calls `load`.

- [ ] **Step 4: Implement the status card and ordered QR flow**

Render status metadata in `.hosted-admin-bili-status`. Render QR inside `<figure>` with expiry `<figcaption>`, followed by an action row containing “二维码确认后继续”, “重新生成”, and “取消”. TOTP is mounted in a separate step beneath the action row. Set QR `width` and `height` attributes and CSS `inline-size:min(100%, 28rem); aspect-ratio:1`.

- [ ] **Step 5: Verify focused tests**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-bili-service-view.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-admin-operation-authorization.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/hosted/admin/bili-service-controller.ts src/hosted/admin/bili-service.ts src/hosted/shell.css tests/hosted-bili-service-view.test.ts tests/hosted-bili-service-admin.test.ts
git commit -m "feat(hosted): clarify Bilibili service replacement feedback"
```

### Task 5: Local production-component simulator

**Files:**
- Create locally, do not stage: `.cache/admin-preview/admin-preview.html`
- Create locally, do not stage: `.cache/admin-preview/main.ts`
- Create locally, do not stage: `.cache/admin-preview/mock-api.ts`
- Create locally, do not stage: `.cache/admin-preview/preview.css`

**Interfaces:**
- Consumes: `mountAdminShell`, `mountAdminOverview`, `mountAccountList`, `mountAdminInvitationView`, `mountBiliServiceView`, and later `mountAdminSettingsView`.
- Produces: an in-memory `MockHostedAdminAPI` whose methods match only the view-facing `Pick<HostedAPI,...>` contracts.

- [ ] **Step 1: Create the local HTML and Vite entry**

The HTML contains `<div id="preview-toolbar"></div><div id="app"></div>` and imports `/src/hosted/shell.css`, `./preview.css`, and `./main.ts`. The toolbar is outside the administrator frame.

- [ ] **Step 2: Implement deterministic mock scenarios**

Provide one account needing a room, two normal accounts, active/used/revoked invitations, a healthy Bilibili service status, a QR challenge, and settings fixtures. Add a latency selector (`0`, `500`, `1500` ms) and “下一次请求失败” toggle. Mutations update cloned in-memory state.

- [ ] **Step 3: Mount each real view from the scenario toolbar**

Buttons switch among `overview`, `accounts`, `invitations`, `bili-service`, and `settings`. A state dropdown switches `normal`, `empty`, and `error`. Remounting disposes the previous view.

- [ ] **Step 4: Start the local preview**

Run: `node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 57904`

Open: `http://127.0.0.1:57904/.cache/admin-preview/admin-preview.html`

Expected: the simulator loads without `/api/` network requests.

- [ ] **Step 5: Verify simulator isolation**

Run: `git status --short`

Expected: `.cache/` remains untracked and no simulator file is staged. Run `node node_modules/vite/bin/vite.js build --config vite.hosted.config.ts` and verify the manifest has only `hosted` and `obs` entries.

### Task 6: First local browser acceptance checkpoint

**Files:**
- Modify only after user feedback: product files from Tasks 1–4.
- Keep screenshots untracked under `.cache/admin-preview/screenshots/`.

**Interfaces:**
- Consumes: the running simulator URL from Task 5.
- Produces: user-approved desktop and mobile layouts before session backend work starts.

- [ ] **Step 1: Inspect all five normal scenarios at desktop size**

Verify buttons visibly react to hover/press, select arrows render, checkboxes remain 20px, notices do not replace tables, check shows busy/success, and QR actions remain under the QR.

- [ ] **Step 2: Inspect empty and error scenarios**

Verify empty invitations show “创建第一个邀请码”; a failed refresh preserves rows; a failed Bilibili check gives retry; no raw exception text is visible.

- [ ] **Step 3: Inspect 390×844**

For every page assert `document.body.scrollWidth === document.body.clientWidth`. Verify controls wrap without overlapping and QR width is at most the content width.

- [ ] **Step 4: Ask the user to review the local simulator**

Keep the server running and provide the local URL. Record feedback as new RED tests before changing product code.

- [ ] **Step 5: Run the first-plan regression gate**

Run:

```text
node node_modules/vitest/vitest.mjs run tests/hosted-admin-interactions.test.ts tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-invitation-view.test.ts tests/hosted-admin-invitations.test.ts tests/hosted-bili-service-view.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-admin-view.test.ts
node node_modules/typescript/bin/tsc --noEmit
node node_modules/vite/bin/vite.js build --config vite.hosted.config.ts
```

Expected: all pass; the production build contains no simulator entry.
