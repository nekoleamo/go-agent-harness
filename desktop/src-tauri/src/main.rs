// gah 桌面壳(P1 落地,可行性见 docs/DESKTOP_FEASIBILITY.md):
// 启动 → spawn sidecar gah --profile web(GAH_WEB_OPEN=0;不传 GAH_HOME env——
// 数据根唯一 = sidecar 二进制同级 gah-data/(便携,首启自动新建),见 cmd/gah homeDir)
//      → 轮询 /api/state 就绪 → 主窗口 navigate http://127.0.0.1:2233
// 单实例(多开 focus 现有窗口);托盘(打开/自启开关/退出);
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

struct Sidecar(Mutex<Option<CommandChild>>);
static READY: AtomicBool = AtomicBool::new(false);

fn httpProbe(path: &str, timeout: Duration) -> bool {
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), timeout) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(400)));
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
    );
    if s.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut buf = [0u8; 64];
    match s.read(&mut buf) {
        Ok(n) => buf[..n].windows(3).any(|w| w == b"200"),
        Err(_) => false,
    }
}

// appDataHome 已移除(2026-09-16):数据根仅允许 sidecar 同级 gah-data/,不再用应用数据目录。

// checkForUpdates 检查更新(tauri-plugin-updater;endpoint 见 tauri.conf plugins.updater):
// 有更新 → 下载安装 + 通知后自动重启;无更新/失败 → 通知(失败不打断,便于未发布期开发)。
async fn checkForUpdates(app: tauri::AppHandle) {
    let result: Result<(), Box<dyn std::error::Error>> = async {
        let updater = app.updater()?;
        if let Some(update) = updater.check().await? {
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
        let _ = app
            .notification()
            .builder()
            .title("gah")
            .body(format!("检查更新失败:{e}"))
            .show();
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

// jsonStr 粗取 JSON 字符串字段值("key":"value";值内无转义即可,探活用)。
fn jsonStr(body: &str, key: &str) -> String {
    let pat = format!("\"{key}\":\"");
    let Some(i) = body.find(&pat) else { return String::new() };
    let rest = &body[i + pat.len()..];
    match rest.find('"') {
        Some(j) => rest[..j].to_string(),
        None => String::new(),
    }
}

// imPhase 当前 IM 连接相位(E 组 E4 托盘通知;未装配 IM 通道 → 空串)。
fn imPhase() -> String {
    let body = httpGET("/api/im/connect/state");
    if body.is_empty() || !body.contains("\"phase\"") {
        return String::new();
    }
    jsonStr(&body, "phase")
}

fn stateRunning() -> bool {
    // /api/state JSON 含 "running":true|false;粗解析含子串即可
    let mut s = match TcpStream::connect_timeout(&GAH_ADDR.parse::<std::net::SocketAddr>().unwrap(), Duration::from_millis(300)) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = s.set_read_timeout(Some(Duration::from_millis(400)));
    let req = format!("GET /api/state HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
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
                        if let Some(w) = handle3.get_webview_window("main") {
                            let _ = w.navigate(GAH_URL.parse().unwrap());
                        }
                        return;
                    }
                    std::thread::sleep(Duration::from_millis(200));
                }
                let _ = handle3.emit("sidecar-start-failed", "gah 60×200ms 内未就绪");
                // 失败指引:窗口不再停留在"正在启动",注入错误提示(数据根=gah-data,需与 gah 同目录且可写)
                if let Some(w) = handle3.get_webview_window("main") {
                    let _ = w.eval("document.body.innerHTML='<div style=\"font-family:-apple-system,sans-serif;padding:40px;max-width:560px\"><h2>gah 服务未能启动</h2><p>数据根为 gah 同目录的 gah-data/(需可写);升级 .app 会替换该目录,如需保留数据请先 /backup。</p><p>详细日志见终端输出。</p></div>'");
                }
            });

            // —— 回合完成通知:轮询 state.running 翻转(running→idle 发通知) ——
            let handle4 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut prev = stateRunning();
                let mut prev_im = imPhase();
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
                    // IM 通道相位通知(E4):失败/需重新登录、连接成功、掉线 三类事件
                    let im = imPhase();
                    if !im.is_empty() && im != prev_im && READY.load(Ordering::SeqCst) {
                        let body = match im.as_str() {
                            "failed" => Some("IM 通道需要处理:凭证失效或校验失败,请在「IM 通道」面板重新连接"),
                            "done" => Some("IM 通道已连接"),
                            "idle" if prev_im == "done" => Some("IM 通道已断开(需重新连接)"),
                            _ => None,
                        };
                        if let Some(text) = body {
                            let _ = handle4.notification().builder().title("gah").body(text).show();
                        }
                    }
                    prev_im = im;
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
                "POST /api/shutdown HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
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
