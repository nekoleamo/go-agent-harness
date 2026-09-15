// gah 桌面壳(P1 落地,可行性见 docs/DESKTOP_FEASIBILITY.md):
// 启动 → spawn sidecar gah --profile web(GAH_WEB_OPEN=0;不传 GAH_HOME env——
// 数据根唯一 = sidecar 二进制同级 gah-data/(便携,首启自动新建),见 cmd/gah homeDir)
//      → 轮询 /api/state 就绪(200;token 模式 401 亦视为就绪) → 主窗口 navigate http://127.0.0.1:2233
// 单实例(多开 focus 现有窗口);托盘(打开/自启开关/退出);
// token 模式(data.auth_token 非空):启动方经 GAH_WEB_TOKEN 传入 token,壳导航到 /#token=…
// (web 侧引导页用它换取 SameSite=Strict cookie),手写 /api/* 请求一并带 gah_token cookie。
// 回合完成通知(轮询 state.running 翻转);退出链:POST /api/shutdown → 等端口释放 →
// 超时 SIGKILL 兜底;RunEvent::Exit 兜底 kill sidecar(信号强杀时 sidecar 变孤儿由
// 启动探测接管:端口已占用则直接 navigate 现有实例)。
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

mod stage;

use tauri::menu::{MenuBuilder, MenuItemBuilder, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Emitter, Manager, RunEvent, WindowEvent};
use tauri_plugin_autostart::MacosLauncher;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_updater::UpdaterExt;
use tauri_plugin_shell::process::CommandChild;
use tauri_plugin_shell::ShellExt;

const GAH_ADDR: &str = "127.0.0.1:2233";
const GAH_URL: &str = "http://127.0.0.1:2233";
// 桌面壳标记:web UI 依 `?shell=desktop` 走桌面壳布局(浏览器端不受影响)。
const GAH_URL_SHELL: &str = "http://127.0.0.1:2233/?shell=desktop";

struct Sidecar(Mutex<Option<CommandChild>>);
// DataRoot 本次运行的**真实**数据根(外置后 = `<用户数据目录>/bin/gah-data`,回退时 = 应用目录内)。
// 升级前备份按它取数(见 backupBeforeUpgrade)。
struct DataRoot(Mutex<Option<PathBuf>>);
static READY: AtomicBool = AtomicBool::new(false);

// web_token 桌面壳用的 Web 访问凭据(启动方经 GAH_WEB_TOKEN 传入;空 = 未开启 token 模式)。
fn web_token() -> String {
    std::env::var("GAH_WEB_TOKEN").unwrap_or_default()
}

// escape_fragment 按 RFC3986 unreserved 转义(引导页侧用 decodeURIComponent 还原,不用 + 语义)。
fn escape_fragment(raw: &str) -> String {
    let mut out = String::new();
    for b in raw.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{:02X}", b)),
        }
    }
    out
}

// cookie_header 裸 TCP 请求用的凭据头(token 模式下 /api/* 需要凭据;空 = 未开启)。
fn cookie_header() -> String {
    let t = web_token();
    if t.is_empty() { String::new() } else { format!("Cookie: gah_token={t}\r\n") }
}

// shell_url 主窗口导航地址:token 模式把凭据放 URL fragment(不发往服务端;引导页换取 cookie)。
fn shell_url() -> String {
    let t = web_token();
    if t.is_empty() { GAH_URL_SHELL.to_string() } else { format!("{GAH_URL_SHELL}#token={}", escape_fragment(&t)) }
}

// status_code 从裸 TCP 响应首行取状态码(解析失败 = 未就绪)。
fn status_code(head: &[u8]) -> Option<u16> {
    let text = String::from_utf8_lossy(head);
    let mut parts = text.split_whitespace();
    parts.next()?; // HTTP/1.1
    parts.next()?.parse::<u16>().ok()
}

// httpProbe 探活:连接成功且状态码 200 或 401(token 模式已就绪)均视为就绪。
fn httpProbe(path: &str, timeout: Duration) -> bool {
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), timeout) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(400)));
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\n{}Connection: close\r\n\r\n",
        cookie_header()
    );
    if s.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut buf = [0u8; 64];
    match s.read(&mut buf) {
        Ok(n) => matches!(status_code(&buf[..n]), Some(200) | Some(401)),
        Err(_) => false,
    }
}

// appDataHome 已移除(2026-09-16):数据根仅允许 sidecar 同级 gah-data/,不用应用数据目录承载
// 数据(那时是「壳告诉 gah 去哪写」)。2026-11-14 换了解法:把 **sidecar 二进制**复制到用户
// 数据目录后再运行,让既有的「二进制同级 gah-data/」规则自己得出应用目录之外的落点 ——
// 数据根解析链一个字没改,但升级不再替换数据、卸装不再删数据(见 stage.rs)。

// backupRoot 升级前备份的落地处:用户主目录下 —— 必须在应用目录之外(升级整包替换应用目录)。
fn backupRoot() -> Option<std::path::PathBuf> {
    let home = std::env::var("HOME").ok().or_else(|| std::env::var("USERPROFILE").ok())?;
    if home.is_empty() { return None; }
    Some(std::path::PathBuf::from(home).join("gah-upgrade-backup"))
}

// backupBeforeUpgrade 升级前把数据根复制到用户主目录(时间戳子目录),返回落地路径。
// 数据根来自本次运行实际使用的位置(外置后应在应用目录外,备份属额外保险)。
// 无数据(首次安装即升级)→ 返回空路径(无可备份);失败 → 错误(调用方中止升级)。
fn backupBeforeUpgrade(data: &std::path::Path) -> Result<std::path::PathBuf, String> {
    if !data.exists() { return Ok(std::path::PathBuf::new()); }
    let root = backupRoot().ok_or("未取到用户主目录(用于放升级前备份)")?;
    let ts = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    let dst = root.join(format!("{ts}"));
    stage::copy_tree(data, &dst.join("gah-data")).map_err(|e| format!("复制 {:?} → {:?} 失败: {e}", data, dst))?;
    Ok(dst)
}

// UpdateOutcome 检查更新的结果:托盘菜单把它转成系统通知(托盘点击后唯一的反馈
// 渠道),界面内入口(invoke check_update)把它原样交给前端 —— 一条逻辑两个出口。
#[derive(Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct UpdateOutcome {
    /// upToDate(已最新) | installed(已装待重启) | failed(检查/安装失败)
    status: &'static str,
    /// 可用/已装版本号(installed 时有值)
    version: Option<String>,
    /// 面向用户的中文说明:界面直接展示,托盘转成通知正文
    message: String,
}

impl UpdateOutcome {
    fn new(status: &'static str, version: Option<String>, message: String) -> Self {
        Self {
            status,
            version,
            message,
        }
    }
}

// checkForUpdates 检查更新(tauri-plugin-updater;endpoint 见 tauri.conf plugins.updater):
// 有更新 → 备份数据 + 下载安装,随后延时重启;无更新/失败 → 返回结果交给调用方呈现。
// 这里不发通知:托盘与界面两个入口的呈现方式不同。
async fn checkForUpdates(app: &tauri::AppHandle) -> UpdateOutcome {
    let outcome = checkForUpdatesInner(app).await;
    // 安装完必须重启才生效。延时一小会儿:界面内入口(前端 invoke)才有机会先把
    // 「已安装」这个结果拿到并渲染出来,否则窗口会在响应返回前就被干掉。
    if outcome.status == "installed" {
        let h = app.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(1500));
            h.restart();
        });
    }
    outcome
}

async fn checkForUpdatesInner(app: &tauri::AppHandle) -> UpdateOutcome {
    let updater = match app.updater() {
        Ok(u) => u,
        Err(e) => return UpdateOutcome::new("failed", None, format!("检查更新未完成:{e}")),
    };
    let update = match updater.check().await {
        Ok(Some(u)) => u,
        Ok(None) => return UpdateOutcome::new("upToDate", None, "已是最新版本".into()),
        Err(e) => return UpdateOutcome::new("failed", None, explainUpdateError(&e.to_string())),
    };
    let version = update.version.clone();
    // 升级整包替换应用目录:数据已外置到用户数据目录(见 stage.rs),正常不会再被替换;
    // 这里仍先备份一次(额外保险,覆盖壳回退到应用目录内运行的极端情况)。
    let data = match app.state::<DataRoot>().0.lock().unwrap().clone() {
        Some(d) => d,
        None => {
            return UpdateOutcome::new("failed", Some(version), "数据根未初始化,已取消升级".into())
        }
    };
    let backup_note = match backupBeforeUpgrade(&data) {
        Ok(p) if p.as_os_str().is_empty() => String::new(),
        Ok(p) => format!("(升级前数据已备份到 {})", p.display()),
        Err(e) => {
            return UpdateOutcome::new(
                "failed",
                Some(version),
                format!(
                    "升级前备份数据失败,为免丢失数据已取消升级:{e}\n请先在对话里执行 /backup(存到应用目录之外的路径),再重试检查更新。"
                ),
            );
        }
    };
    if let Err(e) = update.download_and_install(|_, _| {}, || {}).await {
        return UpdateOutcome::new("failed", Some(version), format!("下载安装未完成:{e}"));
    }
    UpdateOutcome::new(
        "installed",
        Some(version.clone()),
        format!("更新 {version} 已安装,即将重启生效{backup_note}"),
    )
}

// explainUpdateError 把端点错误翻译成人话:404 = 线上还没有 Release(首次发版前/
// 尚未同步),属正常状态而非故障。
fn explainUpdateError(msg: &str) -> String {
    if msg.contains("404") || msg.to_lowercase().contains("not found") {
        "暂无可用更新(线上还没有发布版本)".to_string()
    } else {
        format!("检查更新未完成:{msg}")
    }
}

// notifyUpdate 托盘场景的呈现:系统通知是托盘点击后唯一的反馈渠道。
fn notifyUpdate(app: &tauri::AppHandle, o: &UpdateOutcome) {
    let _ = app.notification().builder().title("gah").body(&o.message).show();
}

// check_update 界面内升级入口(设置面板调用)。
// 桌面壳此前没有任何 invoke 通道,升级只能靠托盘菜单 —— 菜单弹不出来就等于完全
// 没有升级入口(Windows 真机反馈),所以补上这条通道,让 UI 能自己触发并展示结果。
#[tauri::command]
async fn check_update(app: AppHandle) -> UpdateOutcome {
    checkForUpdates(&app).await
}

// httpGETAuth 带凭据的最小 GET(token 模式必须带 cookie,否则 401 → 空串)。
fn httpGETAuth(path: &str) -> String {
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), Duration::from_millis(300)) {
        Ok(s) => s,
        Err(_) => return String::new(),
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(800)));
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\n{}Connection: close\r\n\r\n",
        cookie_header()
    );
    if s.write_all(req.as_bytes()).is_err() {
        return String::new();
    }
    let mut all = Vec::new();
    let mut buf = [0u8; 1024];
    loop {
        match s.read(&mut buf) {
            Ok(0) | Err(_) => break,
            Ok(n) => all.extend_from_slice(&buf[..n]),
        }
    }
    let text = String::from_utf8_lossy(&all).to_string();
    match text.find("\r\n\r\n") {
        Some(i) => text[i + 4..].to_string(),
        None => String::new(),
    }
}

// newScheduleFailures 从 /api/schedules 正文里挑出**新出现的**失败(NOND-W4 无人值守主动通知)。
// seen = id → "last_run_at|last_status" 快照;first=true(启动后第一次轮询)只记不发,
// 避免把启动前就存在的旧失败当新闻推送。返回 (计划名, 错误摘要)。
fn newScheduleFailures(body: &str, seen: &mut HashMap<String, String>, first: bool) -> Vec<(String, String)> {
    let arr: Vec<serde_json::Value> = serde_json::from_str(body).unwrap_or_default();
    let mut out = Vec::new();
    for it in arr {
        let id = it["id"].as_str().unwrap_or("").to_string();
        if id.is_empty() {
            continue;
        }
        let status = it["last_status"].as_str().unwrap_or("");
        let run_at = it["last_run_at"].as_str().unwrap_or("");
        let key = format!("{run_at}|{status}");
        let prev = seen.insert(id, key.clone());
        if first || status != "failed" || prev.as_deref() == Some(key.as_str()) {
            continue;
        }
        let name = it["name"].as_str().filter(|s| !s.is_empty()).unwrap_or("(未命名计划)");
        let err = it["last_error"].as_str().unwrap_or("");
        out.push((name.to_string(), summarize(err, 200)));
    }
    out
}

// summarize 摘要(字符级截断,附省略号;避免系统通知里塞整段报错)。
fn summarize(s: &str, max: usize) -> String {
    let t = s.trim();
    if t.chars().count() <= max {
        return t.to_string();
    }
    let head: String = t.chars().take(max).collect();
    format!("{head}…")
}

// stateRunning 服务是否在运行(/api/state)。
// noticesScript 生成"壳侧提示"注入脚本(纯函数,便于单测)。
//
// 为什么必须注入 DOM:壳侧 emit 的 Tauri 事件只到得了壳自己的 webview 页面,而就绪后窗口
// navigate 到 sidecar 的 http://127.0.0.1:2233(跨源)——事件到不了那边,`runtime-notice`
// 也就成了没有听众的死信号。系统通知(notification().show())在 macOS/Windows 上可能被用户
// 拒绝授权,于是"外置失败/迁移"这类**数据安全相关**的提示会彻底消失。
// 这里用与失败页相同的 w.eval 通道把提示贴进页面(幂等:重复调用只更新同一个节点)。
fn noticesScript(notices: &[String]) -> Option<String> {
    if notices.is_empty() {
        return None;
    }
    let text = serde_json::to_string(&notices.join("\n")).ok()?;
    Some(format!(
        r#"(function(){{var t={text};var id='gah-shell-notice';var el=document.getElementById(id);
if(!el||!document.body){{if(!document.body)return;el=document.createElement('div');el.id=id;
el.style.cssText='position:fixed;left:12px;right:12px;bottom:12px;z-index:99999;background:#fff8e6;border:1px solid #e0b34d;border-radius:8px;padding:10px 12px;font:13px/1.5 -apple-system,sans-serif;color:#3a2c05;white-space:pre-wrap;box-shadow:0 4px 14px rgba(0,0,0,.12)';
var b=document.createElement('span');b.textContent='×';b.style.cssText='float:right;cursor:pointer;padding:0 4px;font-weight:600';
b.onclick=function(){{el.remove()}};el.appendChild(b);document.body.appendChild(el);}}
el.insertBefore(document.createTextNode(t+'\n'),el.firstChild);}})();"#
    ))
}

fn stateRunning() -> bool {
    // /api/state JSON 含 "running":true|false;粗解析含子串即可
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), Duration::from_millis(300)) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(400)));
    let req = format!(
        "GET /api/state HTTP/1.1\r\nHost: 127.0.0.1\r\n{}Connection: close\r\n\r\n",
        cookie_header()
    );
    if s.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut all = Vec::new();
    let mut buf = [0u8; 256];
    loop {
        match s.read(&mut buf) {
            Ok(0) | Err(_) => break,
            Ok(n) => all.extend_from_slice(&buf[..n]),
        }
    }
    let text = String::from_utf8_lossy(&all);
    if let Some(idx) = text.find("\"running\":") {
        let rest = &text[idx + 10..];
        return rest.starts_with("true");
    }
    false
}

// shellLogPath 壳侧诊断日志的落点(<用户数据目录>/gah-shell.log)。
// 取不到用户数据目录时退回系统临时目录 —— 诊断文件写不出去绝不该反过来弄坏启动。
fn shellLogPath(app: &AppHandle) -> PathBuf {
    let dir = app
        .path()
        .app_local_data_dir()
        .unwrap_or_else(|_| std::env::temp_dir());
    dir.join("gah-shell.log")
}

// shellLog 追加一行壳侧诊断(时间戳 + 内容)。
//
// 为何需要:Windows 桌面版没有终端,stderr 无处可去 —— 壳在 setup 里失败时用户只看到
// 「窗口白闪一下就没了」,而机器上不留下任何痕迹,只能靠读代码猜。2026-09-15 v0.1.3 真机
// 白屏就是这样:代码面全部排查无果、机器上取不到证据。写失败一律忽略。
fn shellLog(app: &AppHandle, msg: &str) {
    let path = shellLogPath(app);
    if let Some(d) = path.parent() {
        if std::fs::create_dir_all(d).is_err() {
            return;
        }
    }
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);
    if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(&path) {
        use std::io::Write;
        let _ = writeln!(f, "[{ts}] {msg}");
    }
}

// Runtime 本次运行的落点:要 spawn 的二进制 + 数据根。
// 数据根恒为「二进制同级 gah-data/」(规则不变),只是二进制可能在用户数据目录里。
struct Runtime {
    bin: PathBuf,
    data_root: PathBuf,
    /// true = 已外置到用户数据目录(升级整包替换 / 卸载都不会碰数据)。
    external: bool,
    /// 必须让用户看到的告警(系统通知;不做静默降级)。
    notices: Vec<String>,
}

// siblingDataRoot 随包 sidecar 同级的数据根(= 应用目录内,回退路径的落点)。
fn siblingDataRoot(bin: &std::path::Path) -> PathBuf {
    bin.parent().map(|d| d.join("gah-data")).unwrap_or_else(|| PathBuf::from("gah-data"))
}

// resolveRuntime 决定从哪里跑 sidecar(NOND-W2b-α):
//  ① 正常:复制到 `<用户数据目录>/bin/gah` 再运行 → 「二进制同级 gah-data/」自然落在应用目录外;
//  ② 任一步失败:回退到随包 sidecar(数据仍在应用目录内)并给**显式**告警,绝不静默降级。
fn resolveRuntime(app: &AppHandle) -> Runtime {
    let src = match stage::bundled_sidecar() {
        Ok(p) => p,
        Err(e) => {
            return Runtime {
                bin: PathBuf::new(),
                data_root: PathBuf::from("gah-data"),
                external: false,
                notices: vec![format!("未找到随包运行文件:{e}")],
            }
        }
    };
    let home = match app.path().app_local_data_dir() {
        Ok(p) => p,
        Err(e) => {
            return Runtime {
                data_root: siblingDataRoot(&src),
                bin: src,
                external: false,
                notices: vec![format!(
                    "取用户数据目录失败({e});数据将留在应用目录内,升级或卸载前请先在对话里执行 /backup"
                )],
            }
        }
    };
    let mut notices = Vec::new();
    match stage::migrate_legacy(&home) {
        Ok(Some(p)) => notices.push(format!("旧数据已复制到新位置(旧副本保留):{}", p.display())),
        Ok(None) => {}
        Err(e) => notices.push(format!("旧数据迁移失败(数据未丢失,仍在应用目录内):{e}")),
    }
    match stage::stage_sidecar(&src, &home, env!("CARGO_PKG_VERSION")) {
        Ok(o) => {
            shellLog(
                app,
                &format!(
                    "运行文件{}: {}",
                    if o.staged { "已更新" } else { "已是最新" },
                    o.bin.display()
                ),
            );
            Runtime { bin: o.bin, data_root: stage::data_root(&home), external: true, notices }
        }
        Err(e) => {
            notices.push(format!(
                "无法把运行文件放到用户数据目录({e});本次退回应用目录内运行 —— 升级或卸载可能影响数据,请先 /backup"
            ));
            Runtime { data_root: siblingDataRoot(&src), bin: src, external: false, notices }
        }
    }
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.show();
                let _ = w.set_focus();
            }
        }))
        .plugin(tauri_plugin_autostart::init(
            MacosLauncher::LaunchAgent,
            None,
        ))
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(Sidecar(Mutex::new(None)))
        .manage(DataRoot(Mutex::new(None)))
        // 界面内升级入口:桌面壳此前没有 invoke 通道,升级只能靠托盘菜单,
        // 菜单弹不出来就等于完全没有升级入口(Windows 真机反馈)。
        .invoke_handler(tauri::generate_handler![check_update])
        .setup(|app| {
            let handle = app.handle().clone();
            shellLog(
                app.handle(),
                &format!(
                    "setup 开始: 版本 {} 主程序 {}",
                    env!("CARGO_PKG_VERSION"),
                    std::env::current_exe().map(|p| p.display().to_string()).unwrap_or_else(|_| "?".into())
                ),
            );
            // —— 托盘:打开窗口 / 开机自启开关 / 退出 ——
            let show_item = MenuItemBuilder::with_id("show", "显示窗口")
                .build(app)
                .unwrap();
            let autostart_item = MenuItemBuilder::with_id("autostart", "开机自启")
                .build(app)
                .unwrap();
            let check_item = MenuItemBuilder::with_id("check_update", "检查更新…")
                .build(app)
                .unwrap();
            let quit_item = MenuItemBuilder::with_id("quit", "退出 gah")
                .build(app)
                .unwrap();
            let menu = MenuBuilder::new(app)
                .items(&[
                    &show_item,
                    &autostart_item,
                    &check_item,
                    &PredefinedMenuItem::separator(app).unwrap(),
                    &quit_item,
                ])
                .build()
                .unwrap();
            // 托盘图标必须显式给:TrayIconBuilder 不会自动回退到 default_window_icon。
            // 没有图标时 tray-icon 不设置 NIF_ICON 标志,Shell_NotifyIcon 依然返回成功,
            // 但托盘区留下一个没有图标的空白占位 —— 在 Windows 上就表现为「看不到图标」。
            // 图标与托盘构建失败都**不能是致命错误**(见下方 match):托盘只是便利入口,
            // 界面才是主体;而 setup 一旦 panic 整个进程就退出,可窗口在 setup **之前**
            // 就已创建(tauri 的 app.rs 是先建窗口、再跑 setup),于是用户看到的就是
            // 「窗口白闪一下就没了」—— 界面连一次启动机会都没有(2026-09-15 真机白屏报告)。
            let mut tray_builder = TrayIconBuilder::new()
                .menu(&menu)
                // 显式声明左键也弹菜单:Windows 用户点一下托盘就想看到菜单,
                // 而升级入口只在菜单里(界面上没有),左键不弹 = 找不到升级。
                .show_menu_on_left_click(true)
                .tooltip("gah")
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "show" => {
                        if let Some(w) = app.get_webview_window("main") {
                            let _ = w.show();
                            let _ = w.set_focus();
                        }
                    }
                    "autostart" => {
                        use tauri_plugin_autostart::ManagerExt;
                        let m = app.autolaunch();
                        if m.is_enabled().unwrap_or(false) {
                            let _ = m.disable();
                        } else {
                            let _ = m.enable();
                        }
                    }
                    "check_update" => {
                        // 托盘「检查更新」:异步检查 → 有更新则下载安装并重启;结果经系统通知反馈
                        let h = app.clone();
                        tauri::async_runtime::spawn(async move {
                            let outcome = checkForUpdates(&h).await;
                            notifyUpdate(&h, &outcome);
                        });
                    }
                    "quit" => quitApp(app),
                    _ => {}
                });
                // 不监听 on_tray_icon_event:窗口打开走菜单里的「显示窗口」项(跨平台一致)。
                // 曾经的实现在托盘点击回调里 show()+set_focus(),而 Windows 的托盘菜单走
                // TrackPopupMenu —— 菜单只在自身保持前台时才展开,一旦被别的窗口抢走前台就
                // 立即关闭,于是表现为「左键/右键点了都不弹菜单」(升级入口就在菜单里,
                // 菜单不弹 = 找不到升级)。
                // 顺带:TrayIconEvent::DoubleClick 是 Windows-only,不能拿它当跨平台的
                // 「打开窗口」通道。
            // 图标条件性加上:缺了也只是没有图标,不该让启动失败。
            // (Windows 目标下 tauri-codegen 会从 bundle.icon 挑 .ico 嵌入,Unix 挑 .png,
            // 所以正常情况下一定拿得到;这里只是不再把「拿不到」当成致命的。)
            match app.default_window_icon().cloned() {
                Some(ic) => tray_builder = tray_builder.icon(ic),
                None => shellLog(app.handle(), "未取到应用图标(bundle.icon 需含 .ico/.png),托盘将没有图标"),
            }
            // 托盘构建失败只记日志:它坏掉不应该让整个应用起不来。
            let _tray = match tray_builder.build(app) {
                Ok(t) => {
                    shellLog(app.handle(), "托盘已就绪");
                    let _ = handle.emit("tray-ready", ());
                    Some(t)
                }
                Err(e) => {
                    shellLog(app.handle(), &format!("托盘不可用(已跳过,界面不受影响):{e}"));
                    None
                }
            };

            // —— 数据外置 + spawn sidecar(数据根 = 二进制同级 gah-data/,便携;不传 GAH_HOME env) ——
            let rt = resolveRuntime(app.handle());
            shellLog(
                app.handle(),
                &format!(
                    "运行落点: bin={} data_root={} external={} notices={:?}",
                    rt.bin.display(),
                    rt.data_root.display(),
                    rt.external,
                    rt.notices
                ),
            );
            *app.state::<DataRoot>().0.lock().unwrap() = Some(rt.data_root.clone());
            let _ = handle.emit("runtime-data-root", rt.data_root.display().to_string());
            for n in &rt.notices {
                let _ = app.notification().builder().title("gah").body(n.clone()).show();
                let _ = handle.emit("runtime-notice", n.clone());
            }
            let notices_all = rt.notices.clone();
            // 2233 已被别的进程服务?典型两种:旧版 gah 还驻留在托盘(关窗口 ≠ 退出),
            // 或用户自己开着 `gah --profile web`。这时新起的 sidecar 抢不到端口,界面显示的会是
            // **旧实例** —— 现象极易被误读成「升级没生效」。只记一行日志、不动行为:有它就不必再猜。
            // (连接被拒是立即返回的,不会拖慢正常启动。)
            if httpProbe("/api/state", Duration::from_millis(1200)) {
                shellLog(app.handle(), "注意: 2233 端口已在服务(可能是另一个 gah 实例);若界面显示的是旧实例,请先结束旧进程再重启");
            }
            let cmd = if rt.external {
                app.shell().command(&rt.bin)
            } else {
                app.shell().sidecar("gah").expect("externalBin 缺失: 先运行 scripts/gen-desktop.sh")
            }
            .env("GAH_WEB_OPEN", "0");
            let (mut rx, child) = match cmd.args(["--profile", "web"]).spawn() {
                Ok(v) => v,
                Err(e) => {
                    // 同样不做 panic:窗口留着把失败原因显示出来,远比白屏闪退有用。
                    shellLog(app.handle(), &format!("sidecar 启动失败: {e}"));
                    if let Some(w) = app.get_webview_window("main") {
                        let html = format!(
                            "<div style=\"font-family:-apple-system,sans-serif;padding:40px;max-width:620px\"><h2>gah 服务未能启动</h2><p>启动运行文件失败:{e}</p><p>数据根:{}</p><p>诊断日志:{}</p></div>",
                            rt.data_root.display(),
                            shellLogPath(app.handle()).display()
                        );
                        if let Ok(lit) = serde_json::to_string(&html) {
                            let _ = w.eval(&format!("document.body.innerHTML={lit}"));
                        }
                    }
                    return Ok(());
                }
            };
            shellLog(app.handle(), &format!("sidecar 已启动: {}", rt.bin.display()));
            *app.state::<Sidecar>().0.lock().unwrap() = Some(child);
            let handle2 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                while let Some(ev) = rx.recv().await {
                    if let tauri_plugin_shell::process::CommandEvent::Stderr(line) = ev {
                        let _ = handle2.emit("sidecar-log", String::from_utf8_lossy(&line).trim().to_string());
                    }
                }
            });

            // —— 轮询就绪 → navigate;端口已占用(另一实例在跑)时直接连接接管 ——
            let handle3 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                for _ in 0..60 {
                    if httpProbe("/api/state", Duration::from_millis(400)) {
                        READY.store(true, Ordering::SeqCst);
                        shellLog(&handle3, "sidecar 探活就绪,准备 navigate");
                        let _ = handle3.emit("sidecar-ready", GAH_URL);
                        match shell_url().parse() {
                            Ok(u) => {
                                if let Some(w) = handle3.get_webview_window("main") {
                                    let _ = w.navigate(u);
                                }
                                // 壳侧提示(外置失败/旧数据迁移/…):系统通知可能被拒绝授权,
                                // 且 Tauri 事件跨不到 sidecar 页面 —— 直接贴进页面,重复调用幂等。
                                if let Some(script) = noticesScript(&notices_all) {
                                    let h = handle3.clone();
                                    tauri::async_runtime::spawn(async move {
                                        for _ in 0..6 {
                                            std::thread::sleep(Duration::from_millis(500));
                                            if let Some(w) = h.get_webview_window("main") {
                                                let _ = w.eval(&script);
                                            }
                                        }
                                    });
                                }
                            }
                            Err(e) => {
                                let _ = handle3.emit("sidecar-start-failed", format!("访问地址解析失败: {e}"));
                            }
                        }
                        return;
                    }
                    std::thread::sleep(Duration::from_millis(200));
                }
                shellLog(&handle3, "sidecar 在 60×200ms 内未就绪(注入失败页)");
                let _ = handle3.emit("sidecar-start-failed", "gah 60×200ms 内未就绪");
                // 失败指引:窗口不再停留在"正在启动",注入错误提示(路径经 JSON 编码,避免 Windows 反斜杠/引号破坏 JS)
                let root = handle3
                    .state::<DataRoot>()
                    .0
                    .lock()
                    .ok()
                    .and_then(|g| g.clone())
                    .map(|p| p.display().to_string())
                    .unwrap_or_else(|| "gah-data".into());
                if let Some(w) = handle3.get_webview_window("main") {
                    let html = format!(
                        "<div style=\"font-family:-apple-system,sans-serif;padding:40px;max-width:560px\"><h2>gah 服务未能启动</h2><p>数据根:{root}(需可写)。</p><p>若 config/bundle-web.yaml 里 data.auth_token 非空(token 模式),桌面壳需以环境变量 GAH_WEB_TOKEN 传入同一 token,否则 /api/* 会 401。</p><p>详细日志见终端输出。</p></div>"
                    );
                    if let Ok(lit) = serde_json::to_string(&html) {
                        let _ = w.eval(&format!("document.body.innerHTML={lit}"));
                    }
                }
            });

            // —— 回合完成通知:轮询 state.running 翻转(running→idle 发通知) ——
            let handle4 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut prev = stateRunning();
                loop {
                    std::thread::sleep(Duration::from_secs(2));
                    let cur = stateRunning();
                    if prev && !cur && READY.load(Ordering::SeqCst) {
                        let _ = handle4.notification().builder()
                            .title("gah")
                            .body("回合已完成")
                            .show();
                    }
                    prev = cur;
                }
            });

            // —— 无人值守失败通知:轮询 /api/schedules,出现**新的** failed 终态就弹系统通知
            //    (窗口在托盘里时也能看到;计划失败本体仍只在会话记录与计划列表里)——
            let handle5 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut seen: HashMap<String, String> = HashMap::new();
                let mut first = true;
                loop {
                    std::thread::sleep(Duration::from_secs(5));
                    if !READY.load(Ordering::SeqCst) {
                        continue;
                    }
                    let body = httpGETAuth("/api/schedules");
                    if body.is_empty() {
                        continue; // 未装配 ctx.schedule(503)/网络异常:不当作失败
                    }
                    for (name, err) in newScheduleFailures(&body, &mut seen, first) {
                        let mut msg = format!("计划「{name}」执行失败");
                        if !err.is_empty() {
                            msg.push_str(&format!(":{err}"));
                        }
                        msg.push_str("\n详情见会话记录与「定时任务」列表。");
                        let _ = handle5.notification().builder().title("gah 定时任务失败").body(msg).show();
                    }
                    first = false;
                }
            });

            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if let Some(main) = window.app_handle().get_webview_window("main") {
                    if window.label() == main.label() {
                        api.prevent_close();
                        let _ = window.hide(); // 关窗驻托盘(常驻)
                    }
                }
            }
        })
        .build(tauri::generate_context!())
        .expect("tauri app build 失败")
        .run(|app, event| {
            if let RunEvent::Exit = event {
                if let Some(c) = app.state::<Sidecar>().0.lock().unwrap().take() {
                    if !READY.load(Ordering::SeqCst) {
                        let _ = c.kill(); // 未就绪(启动失败路径):强杀
                    }
                }
            }
        });
}

// quitApp 托盘退出:POST /api/shutdown → 等端口释放(5s) → 仍活 SIGKILL 兜底 → 壳退出。
fn quitApp(app: &AppHandle) {
    let app = app.clone();
    let _ = std::thread::spawn(move || {
        let target = format!("http://{}/api/shutdown", GAH_ADDR);
        // 用 shell 插件 child 无关的最小 HTTP POST(TcpStream 手写)
        if let Ok(mut s) = TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), Duration::from_millis(500)) {
            let body = "{}";
            let req = format!(
                "POST /api/shutdown HTTP/1.1\r\nHost: 127.0.0.1\r\n{}Content-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                cookie_header(),
                body.len(),
                body
            );
            let _ = s.write_all(req.as_bytes());
            let _ = s.read(&mut [0u8; 128]); // 等 resp(200)再关闭
        }
        let _ = target;
        // 等端口释放(≤5s)
        let mut released = false;
        for _ in 0..25 {
            std::thread::sleep(Duration::from_millis(200));
            if !httpProbe("/api/state", Duration::from_millis(200)) {
                released = true;
                break;
            }
        }
        // 兜底:进程清理由主线程 RunEvent::Exit 处理(若 sidecar 已自杀则无需)
        // 强杀残留(gah 未随 shutdown 退出时):经 shell child kill(主线程取 Sidecar 锁)
        if !released {
            // 交由 RunEvent::Exit 的 kill 兜底——但 Exit 在 quit 后触发,直接标记 READY=false
            READY.store(false, Ordering::SeqCst);
        }
        app.exit(0);
    });
}

#[cfg(test)]
mod main_tests {
    use super::*;

    const SAMPLE: &str = r#"[
      {"id":"sched-a","name":"每日备份","last_run_at":"2026-09-12T09:00:00Z","last_status":"failed","last_error":"模型调用超时: dial tcp 10.0.0.1:443: i/o timeout"},
      {"id":"sched-b","name":"周报","last_run_at":"2026-09-12T09:00:00Z","last_status":"ok"},
      {"id":"sched-c","name":"空闲","enabled":true}
    ]"#;

    #[test]
    fn first_poll_records_without_notifying() {
        let mut seen = HashMap::new();
        assert!(newScheduleFailures(SAMPLE, &mut seen, true).is_empty());
        assert_eq!(seen.len(), 3, "三条计划都要进快照(含未运行过的)");
    }

    #[test]
    fn same_failure_is_not_reported_twice() {
        let mut seen = HashMap::new();
        let _ = newScheduleFailures(SAMPLE, &mut seen, true);
        assert!(newScheduleFailures(SAMPLE, &mut seen, false).is_empty(), "同一次失败只报一次");
    }

    #[test]
    fn new_failure_after_bootstrap_notifies() {
        let mut seen = HashMap::new();
        let _ = newScheduleFailures(SAMPLE, &mut seen, true);
        let next = r#"[{"id":"sched-a","name":"每日备份","last_run_at":"2026-09-13T09:00:00Z","last_status":"failed","last_error":"401 未授权"}]"#;
        let got = newScheduleFailures(next, &mut seen, false);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].0, "每日备份");
        assert_eq!(got[0].1, "401 未授权");
    }

    #[test]
    fn ok_and_skipped_states_never_notify() {
        let mut seen = HashMap::new();
        let _ = newScheduleFailures(SAMPLE, &mut seen, false);
        for st in ["ok", "skipped", ""] {
            let body = format!(r#"[{{"id":"sched-b","name":"周报","last_run_at":"2026-09-14T09:00:00Z","last_status":"{st}"}}]"#);
            assert!(newScheduleFailures(&body, &mut seen, false).is_empty(), "状态 {st} 不该通知");
        }
    }

    #[test]
    fn bad_body_is_ignored_not_panicking() {
        let mut seen = HashMap::new();
        assert!(newScheduleFailures("", &mut seen, false).is_empty());
        assert!(newScheduleFailures("定时计划服务未装配", &mut seen, false).is_empty());
        assert!(newScheduleFailures("{\"not\":\"array\"}", &mut seen, false).is_empty());
        assert!(seen.is_empty());
    }

    #[test]
    fn unnamed_schedule_and_long_error_are_handled() {
        let mut seen = HashMap::new();
        let long = "错".repeat(300);
        let body = format!(r#"[{{"id":"sched-x","last_run_at":"t1","last_status":"failed","last_error":"{long}"}}]"#);
        let got = newScheduleFailures(&body, &mut seen, false);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].0, "(未命名计划)");
        assert!(got[0].1.ends_with('…'));
        assert_eq!(got[0].1.chars().count(), 201);
    }

    #[test]
    fn summarize_trims_and_keeps_short_text() {
        assert_eq!(summarize("  a b  ", 10), "a b");
        assert_eq!(summarize("abcdef", 3), "abc…");
    }

    #[test]
    fn notices_script_is_none_when_empty_and_escapes_text() {
        assert!(noticesScript(&[]).is_none());
        let s = noticesScript(&["无法把运行文件放到用户数据目录(磁盘满)".into()]).unwrap();
        assert!(s.contains("gah-shell-notice"), "应注入带 id 的节点: {s}");
        assert!(s.contains("document.body"), "应在 body 可用后才落笔: {s}");
        // 引号/换行/反斜杠必须经 JSON 编码,否则脚本自身会被文案破坏
        let tricky = "路径 \"C:\\x\" 与 \' 单引号\n第二行";
        let s2 = noticesScript(&[tricky.into()]).unwrap();
        assert!(s2.contains(r#"\"C:\\x\""#), "应保留转义后的字面量: {s2}");
        assert!(!s2.contains(r#"路径 "C:"#), "不得裸插引号: {s2}");
        let s3 = noticesScript(&["a".into(), "b".into()]).unwrap();
        assert!(s3.contains("a\\nb"), "多条应换行拼接: {s3}");
    }

    #[test]
    fn sibling_data_root_sits_next_to_bin() {
        assert_eq!(
            siblingDataRoot(std::path::Path::new("/opt/gah/gah")),
            std::path::PathBuf::from("/opt/gah/gah-data")
        );
    }
}
