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

use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

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

// appDataHome 已移除(2026-09-16):数据根仅允许 sidecar 同级 gah-data/,不再用应用数据目录。

// dataRootOfExe sidecar 同级数据根(与 cmd/gah homeDir 同口径:二进制同级 gah-data/)。
// 桌面壳里 sidecar 位于 .app/Contents/MacOS(mac)或安装目录(win),故数据也在应用目录内。
fn dataRootOfExe() -> Option<std::path::PathBuf> {
    let exe = std::env::current_exe().ok()?;
    Some(exe.parent()?.join("gah-data"))
}

// backupRoot 升级前备份的落地处:用户主目录下 —— 必须在应用目录之外(升级整包替换应用目录)。
fn backupRoot() -> Option<std::path::PathBuf> {
    let home = std::env::var("HOME").ok().or_else(|| std::env::var("USERPROFILE").ok())?;
    if home.is_empty() { return None; }
    Some(std::path::PathBuf::from(home).join("gah-upgrade-backup"))
}

// copyTree 递归复制目录(标准库;桌面壳不引新依赖)。
fn copyTree(src: &std::path::Path, dst: &std::path::Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let (from, to) = (entry.path(), dst.join(entry.file_name()));
        if entry.file_type()?.is_dir() { copyTree(&from, &to)?; } else { std::fs::copy(&from, &to)?; }
    }
    Ok(())
}

// backupBeforeUpgrade 升级前把 gah-data 复制到用户主目录(时间戳子目录),返回落地路径。
// 无数据(首次安装即升级)→ 返回空路径(无可备份);失败 → 错误(调用方中止升级)。
fn backupBeforeUpgrade() -> Result<std::path::PathBuf, String> {
    let data = dataRootOfExe().ok_or("解析 sidecar 目录失败")?;
    if !data.exists() { return Ok(std::path::PathBuf::new()); }
    let root = backupRoot().ok_or("未取到用户主目录(用于放升级前备份)")?;
    let ts = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    let dst = root.join(format!("{ts}"));
    copyTree(&data, &dst.join("gah-data")).map_err(|e| format!("复制 {data:?} → {dst:?} 失败: {e}"))?;
    Ok(dst)
}

// checkForUpdates 检查更新(tauri-plugin-updater;endpoint 见 tauri.conf plugins.updater):
// 有更新 → 下载安装 + 通知后自动重启;无更新/失败 → 通知(失败不打断,便于未发布期开发)。
async fn checkForUpdates(app: tauri::AppHandle) {
    let result: Result<(), Box<dyn std::error::Error>> = async {
        let updater = app.updater()?;
        if let Some(update) = updater.check().await? {
            // 升级会整包替换应用目录 → 数据(gah-data 在应用目录内)会一起没掉:
            // 先备份到用户主目录;备份失败则中止升级(绝不拿用户数据冒险)。
            match backupBeforeUpgrade() {
                Ok(p) if p.as_os_str().is_empty() => {}
                Ok(p) => {
                    let _ = app.notification().builder().title("gah 升级前备份").body(format!("数据已备份到:{}", p.display())).show();
                }
                Err(e) => {
                    let _ = app
                        .notification()
                        .builder()
                        .title("gah 升级已取消")
                        .body(format!("升级前备份数据失败,为免丢失数据已取消升级:{e}\n请先在对话里执行 /backup(存到应用目录之外的路径),再重试检查更新。"))
                        .show();
                    return Err(format!("升级前备份失败,已取消升级:{e}").into());
                }
            }
            update.download_and_install(|_, _| {}, || {}).await?;
            let _ = app
                .notification()
                .builder()
                .title("gah")
                .body("更新已安装,即将重启")
                .show();
            app.restart();
        } else {
            let _ = app
                .notification()
                .builder()
                .title("gah")
                .body("已是最新版本")
                .show();
        }
        Ok(())
    }
    .await;
    if let Err(e) = result {
        let msg = e.to_string();
        // 端点 404 = 线上还没有 Release(首次发版前/尚未同步),不是故障:给出明确说法
        let body = if msg.contains("404") || msg.to_lowercase().contains("not found") {
            "暂无可用更新(线上还没有发布版本)".to_string()
        } else {
            format!("检查更新未完成:{msg}")
        };
        let _ = app.notification().builder().title("gah").body(body).show();
    }
}

// httpGET 取一个 GET 响应体的粗文本(裸 TCP;壳只做轻量探活,不引 HTTP 库)。
// 失败返回空串(未就绪/未装配/连不上都归"无数据")。
fn httpGET(path: &str) -> String {
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), Duration::from_millis(300)) {
        Ok(s) => s,
        Err(_) => return String::new(),
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(500)));
    let req = format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
    if s.write_all(req.as_bytes()).is_err() {
        return String::new();
    }
    let mut all = Vec::new();
    let mut buf = [0u8; 512];
    loop {
        match s.read(&mut buf) {
            Ok(0) | Err(_) => break,
            Ok(n) => all.extend_from_slice(&buf[..n]),
        }
    }
    String::from_utf8_lossy(&all).to_string()
}

// stateRunning 服务是否在运行(/api/state)。
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
        .setup(|app| {
            let handle = app.handle().clone();
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
            let _tray = TrayIconBuilder::new()
                .menu(&menu)
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
                        // 托盘「检查更新」:异步检查 → 有更新下载安装并重启;结果经系统通知反馈
                        let h = app.clone();
                        tauri::async_runtime::spawn(async move { checkForUpdates(h).await });
                    }
                    "quit" => quitApp(app),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    use tauri::tray::TrayIconEvent;
                    if let TrayIconEvent::Click { .. } = event {
                        if let Some(w) = tray.app_handle().get_webview_window("main") {
                            let _ = w.show();
                            let _ = w.set_focus();
                        }
                    }
                })
                .build(app)
                .expect("托盘构建失败");
            let _ = handle.emit("tray-ready", ());

            // —— spawn sidecar gah --profile web(数据根 = sidecar 同级 gah-data/,便携;不传 GAH_HOME env) ——
            let cmd = app
                .shell()
                .sidecar("gah")
                .expect("externalBin 缺失: 先运行 scripts/gen-desktop.sh")
                .env("GAH_WEB_OPEN", "0");
            let (mut rx, child) = cmd
                .args(["--profile", "web"])
                .spawn()
                .expect("spawn gah sidecar 失败");
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
                        let _ = handle3.emit("sidecar-ready", GAH_URL);
                        match shell_url().parse() {
                            Ok(u) => {
                                if let Some(w) = handle3.get_webview_window("main") {
                                    let _ = w.navigate(u);
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
                let _ = handle3.emit("sidecar-start-failed", "gah 60×200ms 内未就绪");
                // 失败指引:窗口不再停留在"正在启动",注入错误提示(数据根=gah-data,需与 gah 同目录且可写)
                if let Some(w) = handle3.get_webview_window("main") {
                    let _ = w.eval("document.body.innerHTML='<div style=\"font-family:-apple-system,sans-serif;padding:40px;max-width:560px\"><h2>gah 服务未能启动</h2><p>数据根为 gah 同目录的 gah-data/(需可写);升级 .app 会替换该目录,如需保留数据请先 /backup。</p><p>若 config/bundle-web.yaml 里 data.auth_token 非空(token 模式),桌面壳需以环境变量 GAH_WEB_TOKEN 传入同一 token,否则 /api/* 会 401。</p><p>详细日志见终端输出。</p></div>'");
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
