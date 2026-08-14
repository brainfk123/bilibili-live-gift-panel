# Tencent Cloud Pilot Bootstrap Result

Date: 2026-08-15 (Asia/Shanghai)

## Instance identity

- Tencent Cloud instance ID: `lhins-j4cqq4ao`
- Instance name: `bilibili-live-pilot`
- Region: Shanghai (`ap-shanghai`)
- Public IPv4: `124.220.60.152`
- Operating system: Ubuntu 24.04.4 LTS
- Architecture: `x86_64`
- Root filesystem: `/dev/vda2`, 59 GB total, 51 GB available at baseline
- Memory: 3.6 GiB total, 3.1 GiB available at baseline
- Private interface: `eth0`, `10.0.4.8/22`
- Pre-existing `/opt/bilibili-live`: no
- Pre-existing `bilibili-live.service`: no

## Safety boundary

- The existing Beijing instance `OpenCode-fRh0` was not accessed or modified.
- No credential, cookie, invitation code, OBS token, or database password was read or recorded.
- No public application port was opened during the baseline check.

## Container runtime

- Docker package: `docker.io` 29.1.3 from the Ubuntu 24.04 Tencent package mirror.
- Docker service: enabled and `active`.
- Direct Docker Hub pull: timed out while reaching `registry-1.docker.io`.
- Official Tencent Cloud internal mirror: `https://mirror.ccs.tencentyun.com/` configured in `/etc/docker/daemon.json`.
- Smoke test: `alpine:3.22` downloaded through the mirror and printed `docker-ok` with `--network none`.

## Application account and directories

- Linux account: `biliapp` (`uid=996`, `gid=988`), shell `/usr/sbin/nologin`, member of group `docker`.
- `/opt/bilibili-live`: `0750`, `biliapp:biliapp`.
- `app`, `data`, `logs`, and `backups`: `0750`, `biliapp:biliapp`.
- `secrets`: `0750`, `root:biliapp`.
- Server timezone: `Asia/Shanghai`.

## Connectivity

- Managed MySQL private TCP endpoint `10.0.0.7:3306`: reachable from the application host without using credentials.
- `https://www.bilibili.com/`: reachable, HTTP/2 200 during bootstrap.
- `https://mirror.ccs.tencentyun.com/v2/`: reachable, HTTP/2 200 during bootstrap.

## Public exposure baseline

Tencent Cloud Lighthouse firewall rules present before application deployment:

- TCP 22 from all IPv4 addresses, allow, remark `Linux SSH登录`.
- TCP 80 from all IPv4 addresses, allow, remark `Web服务HTTP (80)，如 Apache、Nginx`.
- All ICMP from all IPv4 addresses, allow, remark `通过Ping测试网络连通性 (放通ALL ICMP)`.
- No TCP 443 rule and no TCP 3306 rule.

Host listener inspection showed only SSH on TCP 22 bound publicly. No process was listening on TCP 80, 443, 3306, or an application port. The existing firewall rules were inspected but not changed.

Public HTTP/HTTPS exposure remains blocked until the server application implements site authentication, invitation binding, one-active-room leases, credential redaction, and six-month audit/log retention.

## Public DNS

DNSPod records created for `bilibililive.cn`, all on the default line with TTL 600:

- `@` A `124.220.60.152` — main site.
- `www` A `124.220.60.152` — main-site alias.
- `app` A `124.220.60.152` — hosted streamer entry point.

An external query through DNSPod Public DNS (`119.29.29.29`) returned `124.220.60.152` for all three names. DNS is active, but no HTTP/HTTPS application or certificate has been deployed yet.

## Landing page local verification

- Static server: `python -m http.server 4173 --directory website` (bound locally to `127.0.0.1` for verification); `GET /` returned HTTP 200.
- Test command: `npx vitest run tests/website.test.ts`; result: 1 test file passed and 6/6 tests passed.
- Exact page title: `礼物互动工坊｜直播互动工具应用`.
- Desktop inspection: 1440×900 viewport showed the hero, `受邀网页版 · 建设中` status, three feature cards, legal statements, and footer clearly. `scrollWidth` equalled `clientWidth` (1425), so no horizontal overflow was observed.
- Mobile inspection: 390×844 viewport showed the same content in a single-column layout. `scrollWidth` equalled `clientWidth` (375), so no horizontal overflow was observed.
- Browser boundary check: no console warnings or errors were captured; the browser asset inventory was empty. The page contains no `<script>`, `<form>`, input, select, textarea, or button elements, so it executes no third-party code and exposes no data-entry or login control.
