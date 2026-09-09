# 桌面壳零成本发行(gah-desktop)

> 定位:C2/C3 的**不花钱方案**(2026-09 决策)。不做 Apple Developer 签名公证与
> Windows 代码签名;应用自更新(updater)用**自持 ed25519 密钥**签名,零成本可用。
> 细节/决策记录见 docs/DESKTOP_FEASIBILITY.md §10 与 DESIGN §14.1。

## 现状(工程侧已铺,待首次 tag 启用)

| 环节 | 状态 | 说明 |
|---|---|---|
| 壳 + sidecar 构建 | ✅ | desktop/ + gen-desktop.sh(本地 debug);发布用 publish-desktop.sh |
| updater 接入 | ✅ | tauri-plugin-updater 已注册 + 托盘「检查更新…」;tauri.conf plugins.updater(端点=GitHub Releases latest.json) |
| 签名密钥 | ✅ | `~/.tauri/gah.key`(ed25519);公钥已入 tauri.conf;私钥**勿入库** |
| 发布脚本 | ✅ | scripts/publish-desktop.sh(单平台构建+签名+JSON)+ merge 模式合成 latest.json |
| CI 矩阵 | ✅ | .github/workflows/release-desktop.yml(mac aarch64 + win x86_64 + merge/上传;tag `v*` 触发) |
| 代码签名(Apple/微软) | ❌ 有意不做 | 首启需一次手动放行,见下 |
| repo 公开 | 待你操作 | 决定 CI runner 免费额度与 GitHub Releases 更新源可用性 |

## 用户首次启动指引(无代码签名代价)

**macOS**(Gatekeeper"无法验证开发者"提示):
```bash
# 方案 A:右键 .app →「打开」→ 确认(仅首次)
# 方案 B:终端去隔离(一次性,可脚本化):
xattr -dr com.apple.quarantine /Applications/gah.app
```
> 仅首次;之后双击正常启动。quarantine 属性来自浏览器/下载,自用构建(不经下载)不受影响。

**Windows**(SmartScreen 蓝色警告):
「更多信息 → 仍要运行」一次;或右键 setup.exe → 属性 → 勾选解除锁定 → 安装。

## 发布流程(发一个版本)

```bash
# 1) 打 tag(驱动 goreleaser(gah 单二进制)与桌面壳同版本)
git tag v0.2.0 && git push origin v0.2.0
```
CI 自动:
1. mac aarch64 / win x86_64 各跑 `scripts/publish-desktop.sh <平台>`(go build sidecar → tauri release bundle → updater 签名 → latest.<平台>.json)
2. merge job 合成 `latest.json`(含两平台 url+signature),连同安装包上传到该 tag 的 GitHub Release
3. 用户端:托盘「检查更新…」→ 从 `https://github.com/nekoleamo/go-agent-harness/releases/latest/download/latest.json` 拉取更新,下载安装后自动重启

**本地发布(不走 CI)**:在 mac/win 机器各跑一次脚本,再在一台机器 `bash scripts/publish-desktop.sh merge`,把 `dist-desktop/` 下产物 + `latest.json` 上传到 GitHub Release(v$VERSION)即可。

## 密钥管理(务必遵守)

```bash
# 生成(已生成于 ~/.tauri/gah.key;若需重建/备份):
npx -y @tauri-apps/cli@2 signer generate -w ~/.tauri/gah.key
cat ~/.tauri/gah.key.pub   # 公钥 → tauri.conf.json plugins.updater.pubkey
```
- **私钥 `gah.key` 绝不入库**;备份到安全处(丢失则旧版无法验签,需全量重装)
- CI 用 `secrets.TAURI_SIGNING_PRIVATE_KEY`(内容=私钥字符串),脚本已支持
- 更新机制是**整包替换**:升级只替换 .app/setup 安装,数据根(应用数据目录)不迁移、不丢

## 已知边界(后续可平滑升级)

- mac 曾用 Intel(x86_64)用户不在当前矩阵:需要时加 macos-13/交叉 job(脚本已支持 darwin-x86_64)
- 有意购证书时:tauri.conf 补 `bundle.macOS.signingIdentity`/公证,脚本不变即可
- updater 更新用 `.app.tar.gz`(mac)/`-setup.exe`(win);未发布版本点「检查更新」会提示失败(端点 404),属预期
