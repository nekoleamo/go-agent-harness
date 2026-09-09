# 桌面端(mac/win)可行性报告:Tauri sidecar 方案

> 状态:已调研 + spike 实测(macOS arm64,2026-09-06)。**壳工程 P1 已落地**(desktop/,2026-09);本报告为决策依据与实施蓝图,落地偏差(数据根等)以 §2/§6/§10 记录为准。
> 决策记录、交付登记见 DESIGN.md §14.1 交付表(桌面化准备行)与 docs/VERIFY.md(M7 Web 节)。

## 0. 结论先行

桌面端**可行,推荐 Tauri v2 sidecar 方案**,理由:gah 前端(Vue3 embed)+ `gah --profile web` 服务形态现成,Tauri 壳把 sidecar `gah` 作为子进程拉起,窗口经 `WebviewUrl` **直接导航 `http://127.0.0.1:<port>`**——同源直连,无 CORS/mixed-content/IPC 依赖,gah 本体零架构改动(仅少量工程性小改,其中停机端点已交付)。

Tauri 用系统 WebView(WKWebView/WebView2),**无浏览器运行时依赖**,不违背"排除 node 分发链、零运行依赖"立身红线;桌面安装包(.dmg/.exe)作为 tar.gz 之外的**第二发行形态**叠加,单二进制线不受影响。

## 1. 背景与目的

`gah web`(本地 127.0.0.1:2233 + 自动开浏览器)已是"单机应用"事实形态。桌面化的价值增量集中在:**独立原生窗口**、**托盘常驻/通知/自启**、**退出即全退(无孤儿进程)**、**一键安装分发**(mac .dmg / win .exe)。目标对标 Claude Desktop 类体验,而内核(gah)保持 CGO=0 单二进制交付不变。

## 2. 现状盘点(代码证据)

| 项 | 现状 | 含义 |
|---|---|---|
| 前端 | web-src(Vue3+Vite+TS)→ go:embed `web/dist` | Tauri 无需打包前端,窗口直指 gah 服务即可 |
| 服务形态 | `gah web` ≡ `--profile web`;SSE/WS+REST+静态 embed 自包含 | 壳只需拉起进程 + WebView 指 URL |
| 无 TTY 运行 | 非 TTY 拒绝仅限 `tui` profile;profile-web 不含 ui-tui-app | 后台无头运行现成可用(实测) |
| 停机 | SIGINT/SIGTERM → DisposeAll;**Windows 无 SIGTERM** | 需跨平台停机端点(已交付 `POST /api/shutdown`) |
| 数据根 | 壳 spawn sidecar 时**恒传 `GAH_HOME`**:显式 env > 系统应用数据目录 `appDataHome()`(macOS ~/Library/Application Support/gah;Linux XDG_DATA_HOME|~/.local/share/gah;Windows %APPDATA%\gah) | `.app` 内便携根(Contents/MacOS/gah-data)随升级丢失且可能只读,**不作桌面默认**;与 CLI 便携形态(gah-data)分根,无文件竞争 |
| 鉴权 | auth_token 可选;静态不鉴权;前端无 token 逻辑 | 桌面壳维持留空(仅本机绑定)即当前默认可用 |
| 分发 | goreleaser 六目标(darwin/linux/win × amd64/arm64;win/arm64 ignore) | 壳矩阵取 darwin×2 + win×amd64,gah 产物现成 |

## 3. 技术路线对比

| 路线 | 评价 | 结论 |
|---|---|---|
| **Tauri v2 sidecar** | 系统 WebView,零浏览器依赖;前端/API 复用;壳管生命周期/托盘/通知/自启/单实例;产物 .dmg/.exe | ✅ **采用** |
| PWA | 最轻(manifest+SW),浏览器"安装为应用";托盘/自启/通知受限 | 不采用(用户已排除;可作远期补充) |
| 纯 Go systray + 浏览器 | mac/win 托盘需 cgo,击穿 CGO_ENABLED=0 + 交叉编译红线;体验仍非独立窗口 | 不采用 |
| Electron | 自带 Chromium+node 分发链,与项目"排除 node"立身目的正面冲突 | 排除 |
| Wails | Go + 系统 webview,但需 CGO(mac objc / win WebView2 COM),撞红线 | 排除 |

## 4. 架构:R2 同源直连 + sidecar

```
┌─ gah.app(壳,Tauri v2)──────────────────────────┐
│ Rust 侧:spawn sidecar gah --profile web        │
│        → 轮询 GET /api/state 就绪              │
│        → 主窗口 navigate http://127.0.0.1:<port>│
│        → 托盘/通知/自启/单实例(全 Rust,免 IPC) │
│ 退出:POST /api/shutdown → 等端口释放 →          │
│      超时 SIGKILL 兜底;信号强杀由启动探测兜底   │
└──────────────┬──────────────────────────────────┘
               │(子进程,env: GAH_HOME、GAH_WEB_OPEN=0)
        ┌──────▼──────┐
        │ gah 单二进制 │ ← 数据落 GAH_HOME(壳显式传入的应用数据目录,稳定可写)
        └─────────────┘
```

- **R2(窗口直指 localhost)选型理由**:同源 → 无需给 gah 加 CORS、无 mixed content、前端零改动;gah 前端不 import 任何 `@tauri-apps/api`(纯浏览器实现),remote 页无 IPC 诉求,capability 约束天然绕开。
- **事件驱动通知**:壳 Rust 侧订阅 gah `GET /api/events/ws`(SSE 亦可)感知 running 状态 → 回合完成系统通知;不依赖页面 IPC。

## 5. Spike 实测记录(macOS arm64,2026-09-06)

工程:`/tmp/gah-spike`(Tauri v2 + sidecar `gah-aarch64-apple-darwin`)。结果:

| # | 验证项 | 结果 | 证据 |
|---|---|---|---|
| 1 | sidecar spawn gah(`--profile web`,隔离 GAH_HOME) | ✅ | 插件进程起于 home/plugins,服务可用 |
| 2 | **WKWebView 直连 http://127.0.0.1:2233** | ✅ | WebKit.Networking 与 gah 保持 14 条连接(keep-alive+事件通道);WebContent 渲染进程 ~100MB RSS |
| 3 | 服务面(state/SSE/WS) | ✅ | `/api/state` 200;SSE `/api/events` 200 hold-open;WS `/api/events/ws` open |
| 4 | gah SIGTERM 优雅退出 | ✅ | 主进程干净退出、端口释放 |
| 5a | 壳正常退出清理 sidecar | ⚠️ | RunEvent::Exit 路径有效;**信号强杀壳时不触发 Exit → gah 及插件孤儿**(需启动探测/信号 handler 兜底) |
| 5b | gah 退出回收外部插件 | ✅ | dispose 链完整(受控 SIGTERM 实测 tool-* 全回收;早前"残留"系信号未达宿主之误判) |

**环境注意**:cargo 全局镜像(ustc git)失效 → 项目级 `.cargo/config.toml` 切 tuna https 解决(临时 spike 配置,不入仓库)。

## 6. gah 侧改动清单

| 类别 | 内容 | 状态 |
|---|---|---|
| 必须① 停机端点 | `web.Server.OnShutdown` + `POST /api/shutdown`(ui-web-app 绑定 `system/shutdown`);Windows 无 SIGTERM、壳优雅退出统一通道 | ✅ **已交付**(DESIGN §14.1 交付表) |
| 必须② 动态端口 | ui-web-app 支持 `GAH_WEB_ADDR` env(对齐 GAH_WEB_STATIC/OPEN 先例);监听失败显式信号 | ⏳ 壳工程前做(多实例/端口冲突) |
| 建议③ | 前端补 token 携带(读 `?token=`→sessionStorage/cookie)以支持 auth_token 桌面场景;默认空 token 可不做 | ⏳ 可选安全增强 |
| 不改 | CORS(同源不需要)、bundle/profile(复用 profile-web) | — |

> **落地偏差(2026-09-16)**:§2 原「数据布局不改(共享 ~/.gah)」已变更——壳恒传 GAH_HOME(显式 env > 应用数据目录),与 CLI 便携分根;见 §10 决策记录。

## 7. 分发矩阵

| | mac | win |
|---|---|---|
| 壳目标 | aarch64(主力)+ x86_64 | x86_64(gah 无 win/arm64) |
| sidecar triple | `aarch64-apple-darwin` / `x86_64-apple-darwin` | `x86_64-pc-windows-msvc` |
| 产物 | .app + .dmg | NSIS -setup.exe(WebView2 Win10/11 系统自带) |
| 签名 | 必须:Developer ID + hardened runtime + notarization(CI secrets:APPLE_CERTIFICATE 等) | 可选(仅 SmartScreen) |
| 体积预估 | ~35MB(壳 ~3MB + gah 31.9MB) | 同左,WebView2 系统自带 |

版本策略:Tauri updater 整包替换 → **gah 版本与壳版本绑定发布**;更新需自托管 endpoint + 签名密钥。

## 8. 风险与未决项

1. WKWebView 直连 http://127.0.0.1 的剪贴板/WS 深水区行为(spike 已证连接/渲染/事件通道;剪贴板 API 需真机交互确认)
2. 双实例竞争:壳 + 终端 CLI 同跑(数据分根无文件竞争;2233 端口冲突由就绪轮询「端口已占用 → 直接 navigate 接管」兜底)
3. 壳被强杀留孤儿:启动探测端口占用 / Rust 信号 handler
4. Windows 真机验证(无 arm64 本机)与 macOS 签名/公证流水线成本

## 9. 里程碑建议(暂缓,按需启用)

| 阶段 | 内容 | 量级 |
|---|---|---|
| P0 | spike 已过;gah 侧改动②(GAH_WEB_ADDR) | S |
| P1 | `desktop/` Tauri 工程:sidecar spawn + ready 轮询 + navigate + 托盘/通知/自启/单实例 + 退出链(shutdown→兜底 kill) | L ✅ 已落地(2026-09) |
| P2 | mac 签名+公证、win NSIS、CI 矩阵(复用 goreleaser 产物做 sidecar) | L 🚧 **零成本变体已铺(2026-09-16)**:不购证书——publish-desktop.sh(无签名 bundle + updater 自持签名)+ release-desktop.yml(公开 repo 免费 runner,tauri.conf 增 nsis);证书方案可按需平滑叠加 |
| P3 | updater 更新通道(需自托管 + 签名) | M 🚧 **已接入(2026-09-16)**:tauri-plugin-updater + 托盘「检查更新」+ ed25519 自持密钥 + GitHub Releases 为端点(免自托管) |

## 10. 决策记录

- 2026-09-06:确定 **Tauri v2 sidecar + R2 同源直连**;排除 Electron/Wails/纯 systray;不采用 PWA。
- 2026-09-06:spike 实证关键链路(mac arm64),验证报告此文档;gah 侧停机端点先行交付。
- 2026-09-16:**P1 壳工程落地**(desktop/src-tauri/main.rs:spawn sidecar + 就绪轮询 navigate + 托盘/通知/自启/单实例 + 退出链)。**数据根决策变更**:壳 spawn 恒传 GAH_HOME(显式 env > 系统应用数据目录 appDataHome()),弃早期「共享 ~/.gah / .app 内便携根」——便携根随升级丢失且可能只读;启动 12s 未就绪窗口注入失败提示(含数据根)。cargo check 通过。
- 2026-09-16(零成本发行决策):**不购 Apple Developer / Windows 签名证书**;C2/C3 以零成本形态推进——updater 用 ed25519 自持密钥(tauri signer,~/.tauri/gah.key,公钥入 conf)+ GitHub Releases(公开 repo)为更新端点;工程侧已铺:tauri-plugin-updater 注册与托盘「检查更新」、tauri.conf nsis target、scripts/publish-desktop.sh(构建+签名+latest.json/merge)、.github/workflows/release-desktop.yml(mac aarch64 + win x86_64 + merge 上传, tag v* 触发);无签名分发指引 docs/RELEASE.md(右键打开/xattr 去隔离/SmartScreen)。代价:用户首次启动一次手动放行;repo 需公开(用户已确认)。
- P1 壳工程已按 P0→P3 落地(2026-09);P2 签名/公证、P3 updater 仍待用户排期。
