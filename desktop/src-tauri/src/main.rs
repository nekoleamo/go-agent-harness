// gah 桌面壳(P1 落地,可行性见 docs/DESKTOP_FEASIBILITY.md):
// 启动 → spawn sidecar gah --profile web(GAH_WEB_OPEN=0);GAH_HOME 未显式设置时由
// sidecar 主程序解析:二进制同级 gah-data/(便携根,首启自动新建;Tauri 壳内即
// Contents/MacOS/gah-data);不可便携则报错退出——~/.gah/TempDir 兜底已弃用(2026-09)
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
            let quit_item = MenuItemBuilder::with_id("quit", "退出 gah")
                .build(app)
                .unwrap();
            let menu = MenuBuilder::new(app)
                .items(&[
                    &show_item,
                    &autostart_item,
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

            // —— spawn sidecar gah --profile web(GAH_HOME 不设=便携根解析链:二进制同级 gah-data/ 优先) ——
            let home = std::env::var("GAH_HOME").unwrap_or_default();
            let mut cmd = app
                .shell()
                .sidecar("gah")
                .expect("externalBin 缺失: 先运行 scripts/gen-desktop.sh")
                .env("GAH_WEB_OPEN", "0");
            if !home.is_empty() {
                cmd = cmd.env("GAH_HOME", &home); // 显式 GAH_HOME(测试/便携部署)优先
            }
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
