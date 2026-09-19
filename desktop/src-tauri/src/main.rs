// gah 桌面壳(P1 落地,可行性见 docs/DESKTOP_FEASIBILITY.md):
// 启动 → spawn sidecar gah --profile web(GAH_WEB_OPEN=0;不传 GAH_HOME env——
// 数据根唯一 = sidecar 二进制同级 gah-data/(便携,首启自动新建),见 cmd/gah homeDir)
//      → 轮询 /api/state 就绪(200;token 模式 401 亦视为就绪) → 主窗口 navigate http://127.0.0.1:2233
// 单实例(多开 focus 现有窗口);托盘(打开/自启开关/退出);
// token 模式(data.auth_token 非空):启动方经 GAH_WEB_TOKEN 传入 token,壳导航到 /#token=…
// (web 侧引导页用它换取 SameSite=Strict cookie),手写 /api/* 请求一并带 gah_token cookie。
// 通知(NOND-N1/N2):单条 2s 轮询消费宿主提示流 /api/notices(warn/error → 系统通知),
// 同一轮询里看 state.running 翻转发「回合已完成」(宿主不发这类提示,见 notice.rs 注释);
// 退出链:POST /api/shutdown → 等端口释放 →
// 超时 SIGKILL 兜底;RunEvent::Exit 兜底 kill sidecar(信号强杀时 sidecar 变孤儿由
// 启动探测接管:端口已占用则直接 navigate 现有实例)。
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

mod notice;
mod stage;

use tauri::menu::{CheckMenuItem, CheckMenuItemBuilder, MenuBuilder, MenuItem, MenuItemBuilder, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Emitter, Manager, RunEvent, WindowEvent};
use tauri_plugin_autostart::MacosLauncher;
use tauri_plugin_autostart::ManagerExt;
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_updater::UpdaterExt;
use tauri_plugin_shell::process::CommandChild;
use tauri_plugin_shell::ShellExt;

// 默认 Web 地址:仅在「挑不到空闲端口」时兜底。桌面壳正常走自己挑的私有端口(见 pick_free_port),
// 因为固定 2233 会撞上**别人的**实例 —— 白屏崩溃留下的孤儿 sidecar、用户自己开的 `gah web`,
// 都能让壳连上去,于是「装的明明是新版,界面却没有新功能」(2026-09-16 真机:升级后设置里
// 找不到「关于 gah」,实际是窗口显示着旧实例的页面)。sidecar 侧由 GAH_WEB_ADDR 覆写
// (plugins/ui/ui-web-app/web.go 早已预留这个口,只是壳一直没用)。
const GAH_ADDR: &str = "127.0.0.1:2233";
// 桌面壳标记:web UI 依 `?shell=desktop` 走桌面壳布局(浏览器端不受影响)。
const GAH_SHELL_PATH: &str = "/?shell=desktop";

// WEB_ADDR 本次运行的 Web 地址,setup 早期写入一次(此后只读,故用 OnceLock)。
static WEB_ADDR: std::sync::OnceLock<String> = std::sync::OnceLock::new();

// web_addr 本次运行的 Web 地址(未写入时回退默认端口)。
fn web_addr() -> String {
    WEB_ADDR.get().cloned().unwrap_or_else(|| GAH_ADDR.to_string())
}

// web_url 本次运行的 Web 根地址。
fn web_url() -> String {
    format!("http://{}", web_addr())
}

// web_sockaddr 本次运行的 Web 地址(供裸 TCP 探活/关机请求用)。
fn web_sockaddr() -> std::net::SocketAddr {
    web_addr()
        .parse()
        .unwrap_or_else(|_| GAH_ADDR.parse().expect("默认地址常量必须可解析"))
}

// pick_free_port 向内核要一个空闲端口(绑 127.0.0.1:0 读回实际端口后立即释放)。
// 与真正绑定之间有极小的竞态窗口,可接受:真被抢也只是这次启动失败、重开一次。
fn pick_free_port() -> String {
    let Ok(l) = std::net::TcpListener::bind("127.0.0.1:0") else {
        return GAH_ADDR.to_string();
    };
    let addr = l
        .local_addr()
        .map(|a| a.to_string())
        .unwrap_or_else(|_| GAH_ADDR.to_string());
    drop(l);
    addr
}

struct Sidecar(Mutex<Option<CommandChild>>);
// TrayAutostart 托盘里的「开机自启」勾选项:切换后必须把勾选态回写成实际状态,
// 否则用户点完看不出任何变化(=「点了没反应」)。
struct TrayAutostart(Mutex<Option<CheckMenuItem<tauri::Wry>>>);
// TrayCheck 托盘里的「检查更新…」项:检查期间要能立刻变成「检查更新中…」并禁用。
struct TrayCheck(Mutex<Option<MenuItem<tauri::Wry>>>);
// 检查更新项的两种文字(集中一处,免得改文案漏掉一边)
const CHECK_IDLE_TEXT: &str = "检查更新…";
const CHECK_BUSY_TEXT: &str = "检查更新中…";
// PickSlot 文件夹选择器的一次运行状态(begin 置位,poll 取结果并复位)。
//
// 为什么不直接用 async 命令 await blocking_pick_folder(2026-09-17 真机):那台机器上
// async 命令**从未进入函数体**(壳日志里连入口行都没有),前端 await 永久挂起 —— 表现就是
// 「点 ＋ 打开 / 浏览… 没反应」。同步命令(shell_probe/shell_log)同机是通的,于是改成
// 「同步命令 + 独立线程 + 轮询」:只依赖已被真机验证的通道,阻塞的也是自建线程,
// 不再占 async 运行时的工作线程(那正是怀疑中的连环卡死源:一个卡住全卡)。
#[derive(Default)]
struct PickSlot {
    running: bool,
    done: bool,
    path: Option<String>,
}
struct PickState(Mutex<PickSlot>);
// 检查更新的「轮次」与「完成水位」:看门狗按轮次判断自己那一轮是否真的回来了。
static CHECK_SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static CHECK_DONE: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
// sidecar stdout 已落日志的行数上限(它是 go-plugin 的二进制 RPC 通道,只兜「文本日志」)。
static STDOUT_LOGGED: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
// panic 钩子用的日志路径(钩子拿不到 AppHandle,只能在 setup 里提前塞进来)。
static LOG_PATH: std::sync::OnceLock<PathBuf> = std::sync::OnceLock::new();
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
    let base = format!("{}{}", web_url(), GAH_SHELL_PATH);
    let t = web_token();
    if t.is_empty() { base } else { format!("{base}#token={}", escape_fragment(&t)) }
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
    let mut s = match TcpStream::connect_timeout(&web_sockaddr(), timeout) {
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
// 数据(那时是「壳告诉 gah 去哪写」)。2026-09-12 换了解法:把 **sidecar 二进制**复制到用户
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

// notifyUpdate 托盘场景的呈现:系统通知 + 原生对话框双通道。
// 系统通知可能根本不来(未授权/被系统设置拦/专注模式),而托盘里点「检查更新…」之后
// 「什么都没发生」是最糟的反馈 —— 真机反馈正是如此(2026-09-16)。对话框必然可见,
// 通知作为余量保留(窗口最小化时它更轻)。
fn notifyUpdate(app: &tauri::AppHandle, o: &UpdateOutcome) {
    let _ = app.notification().builder().title("gah").body(&o.message).show();
    let _ = app.dialog().message(&o.message).title("gah 检查更新").show(|_| {});
}

// UpdateSnapshot 检查更新的当前状态 —— 托盘与设置面板两个视图的**单一真源**。
//
// 为何要它:2026-09-17 真机反馈「设置界面开着时,从托盘勾开机自启或点检查更新,设置界面
// 不同步」。根因是两个视图各拿各的局部状态。这里把「进行中 / 第几轮 / 结论」记在壳里,
// 页面开着时轮询 `update_state` 即可与托盘动作实时对齐。
#[derive(Clone)]
struct UpdateSnapshot {
    busy: bool,
    seq: u64,
    status: &'static str,
    message: String,
    version: Option<String>,
}

static UPDATE_STATE: Mutex<UpdateSnapshot> = Mutex::new(UpdateSnapshot {
    busy: false,
    seq: 0,
    status: "",
    message: String::new(),
    version: None,
});

// updateJson 把快照编码成前端契约的 JSON。单独成函数并配单测:它是两个视图的对齐接口,
// 形状错了只会表现为「界面没反应」。
fn updateJson(s: &UpdateSnapshot) -> String {
    let msg = serde_json::to_string(&s.message).unwrap_or_else(|_| "\"\"".to_string());
    let ver = match s.version.as_deref() {
        Some(v) => serde_json::to_string(v).unwrap_or_else(|_| "null".to_string()),
        None => "null".to_string(),
    };
    format!(
        "{{\"busy\":{},\"seq\":{},\"status\":\"{}\",\"message\":{},\"version\":{}}}",
        s.busy, s.seq, s.status, msg, ver
    )
}

fn setUpdateBusy(busy: bool) {
    if let Ok(mut g) = UPDATE_STATE.lock() {
        g.busy = busy;
    }
}

// recordUpdate 记一次检查的结论(托盘路径与命令路径共用)。
fn recordUpdate(seq: u64, o: &UpdateOutcome) {
    if let Ok(mut g) = UPDATE_STATE.lock() {
        g.busy = false;
        g.seq = seq;
        g.status = o.status;
        g.message = o.message.clone();
        g.version = o.version.clone();
    }
}

// update_state 前端轮询用(设置面板开着时每 1.5 秒一次):同步命令、不写日志(轮询会刷屏)。
#[tauri::command]
fn update_state() -> String {
    match UPDATE_STATE.lock() {
        Ok(g) => updateJson(&g),
        Err(_) => updateJson(&UpdateSnapshot {
            busy: false,
            seq: 0,
            status: "failed",
            message: "壳内更新状态锁不可用".into(),
            version: None,
        }),
    }
}

// check_update 界面内升级入口(设置面板调用)。
// 桌面壳此前没有任何 invoke 通道,升级只能靠托盘菜单 —— 菜单弹不出来就等于完全
// 没有升级入口(Windows 真机反馈),所以补上这条通道,让 UI 能自己触发并展示结果。
#[tauri::command]
async fn check_update(app: AppHandle) -> UpdateOutcome {
    let seq = CHECK_SEQ.fetch_add(1, Ordering::SeqCst) + 1;
    shellLog(&app, &format!("web: 检查更新开始(第 {seq} 轮)"));
    setUpdateBusy(true);
    // 命令路径同样挂看门狗:任务不回来时至少能解除进行中并给出结论(与托盘路径一致)。
    startCheckWatchdog(&app, seq);
    let t0 = std::time::Instant::now();
    let o = checkForUpdates(&app).await;
    shellLog(
        &app,
        &format!(
            "web: 检查更新返回 status={} 耗时={}ms",
            o.status,
            t0.elapsed().as_millis()
        ),
    );
    CHECK_DONE.fetch_max(seq, Ordering::SeqCst);
    recordUpdate(seq, &o);
    o
}

// spawnAutoCheck 升级冒烟缝:GAH_SHELL_UPDATE_AUTOCHECK=<秒> 时,启动后自动跑一次「检查更新」,
// 走与托盘菜单/界面按钮**完全同一条**代码路径(checkForUpdates → 备份 → download_and_install →
// 延时重启),因此能把「发出去的包到底能不能升级」从「只能靠人点托盘」变成一条可脚本化的命令。
//
// 为何需要(2026-09-18):updater 的真机路径此前每次都要人手点、且点完还得人肉确认版本与数据 ——
// 「装机自动升级端到端」因此长期挂在待办里。加这条缝之后,冒烟可以这样跑(见 docs/RELEASE.md):
//
//   RELEASE_VERSION=0.1.3 GAH_DESKTOP_DEBUG=1 bash scripts/publish-desktop.sh darwin-aarch64
//   GAH_SHELL_UPDATE_AUTOCHECK=5 "<旧版>/gah.app/Contents/MacOS/gah-desktop"
//
// 默认关闭:没设这个变量时一行代码都不多跑。装完新版后会重启:若环境变量被一并继承(重启沿用当前
// env),下一轮调到的已经是**新版自身**,而那一轮远端已是「已是最新」→ 不会再装,不存在自升级死循环。
fn spawnAutoCheck(app: AppHandle) {
    let Ok(raw) = std::env::var("GAH_SHELL_UPDATE_AUTOCHECK") else {
        return;
    };
    if raw.is_empty() || raw == "0" {
        return;
    }
    let secs: u64 = raw.parse().unwrap_or(3);
    shellLog(
        &app,
        &format!("升级冒烟:GAH_SHELL_UPDATE_AUTOCHECK={raw}({secs} 秒后自动检查更新)"),
    );
    let _ = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_secs(secs));
        let seq = CHECK_SEQ.fetch_add(1, Ordering::SeqCst) + 1;
        shellLog(&app, &format!("升级冒烟: 检查更新开始(第 {seq} 轮)"));
        setCheckBusy(&app, true);
        // 刻意**不挂 75 秒看门狗**:真实下载 30+ MB 可能超过它,那只会在冒烟里造成
        // 「看门狗先报失败」的假警报(托盘路径的 45 秒超时同理放宽到 300 秒)。
        let h = app.clone();
        tauri::async_runtime::spawn(async move {
            let t0 = std::time::Instant::now();
            let outcome = match tokio::time::timeout(Duration::from_secs(300), checkForUpdates(&h))
                .await
            {
                Ok(o) => o,
                Err(_) => UpdateOutcome::new("failed", None, "检查更新超时(300 秒未返回)".into()),
            };
            shellLog(
                &h,
                &format!(
                    "升级冒烟: 检查更新结束 status={} version={:?} 耗时={}ms msg={}",
                    outcome.status,
                    outcome.version,
                    t0.elapsed().as_millis(),
                    outcome.message
                ),
            );
            CHECK_DONE.fetch_max(seq, Ordering::SeqCst);
            setCheckBusy(&h, false);
            recordUpdate(seq, &outcome);
            // 装完的延时重启由 checkForUpdates 自己发起(与托盘/界面路径一致),这里不再插手。
        });
    });
}

// shell_probe / probe_async 自检用的最小命令:验证前端经 withGlobalTauri 究竟能不能调进壳。
//
// 为何需要:本项目没有任何 capabilities/ 文件,而 plugins 的 JS 全局 API 在页面上是活的
// (withGlobalTauri 注入),拿它一调就是 ACL 拒绝(实测到 notification.is_permission_granted
// 被拒)。应用自有命令是否同样被拦,只能实测 —— 而界面上的「检查更新」走的正是这条通道。
// 不调 check_update 本体去探测,是因为那会真的发网络请求、甚至真去装更新。
#[tauri::command]
fn shell_probe() -> String {
    "ok".to_string()
}

// probe_async 异步命令通道探针:只写一行「函数体已执行」并回 ok。
//
// 为何单独要它:2026-09-17 真机上 async 命令(pick_folder)连入口日志都没留下,而同步命令
// 全通。到底是「异步任务没被调度」还是「请求没到处理函数」,只能靠这一行区分 ——
// 页面启动时自检里调一次,日志里有没有这行就是答案。
#[tauri::command]
async fn probe_async(app: AppHandle) -> String {
    shellLog(&app, "web: probe_async 函数体已执行(异步命令通道可达)");
    "ok".to_string()
}

// pick_folder_begin / pick_folder_poll 系统文件夹选择器(界面「＋ 打开」/「浏览…」调用)。
//
// 为什么是三条命令而不是一条 async 命令(2026-09-17 真机实测):
//   ① 走 plugin:dialog|open 的 JS 插件通道 —— 点击毫无反应,页面上也无可见报错;
//   ② 改走壳自有 async 命令 pick_folder —— 依旧无反应,而且壳日志里**连入口行都没有**
//      (函数体没执行过),前端 await 永久挂起。同机同步命令(shell_probe/shell_log)全通;
//   ③ 于是改为同步命令 + 独立线程 + 轮询:begin 起线程做阻塞选择,poll 取结果。
// 全程写日志:用户说「点了没反应」时,这几行是唯一能自证「到底走到哪一步」的证据。
#[tauri::command]
fn pick_folder_begin(app: AppHandle, title: Option<String>) -> String {
    let title = title.unwrap_or_else(|| "选择工作区目录".to_string());
    {
        let st = app.state::<PickState>();
        let mut g = st.0.lock().unwrap();
        if g.running {
            shellLog(&app, "web: 选择器已在进行中,忽略重复点击");
            return "busy".to_string();
        }
        *g = PickSlot {
            running: true,
            ..Default::default()
        };
    }
    shellLog(&app, "web: 文件夹选择器线程启动");
    let h = app.clone();
    // 独立线程里做阻塞式选择:blocking_pick_folder 的官方约束正是「不得在主线程调用」,
    // 普通线程是它期望的位置(内部走 run_on_main_thread 弹原生框)。
    std::thread::spawn(move || {
        let picked = h.dialog().file().set_title(title).blocking_pick_folder();
        let out = picked.map(|p| p.to_string());
        shellLog(
            &h,
            &format!(
                "web: 文件夹选择器返回 {}",
                out.clone().unwrap_or_else(|| "(取消)".into())
            ),
        );
        let mut g = h.state::<PickState>();
        let mut g = g.0.lock().unwrap();
        g.running = false;
        g.done = true;
        g.path = out;
    });
    "started".to_string()
}

// pickJson 把一次选择结果编码成前端契约的 JSON。
// 单独成函数并配单测:这串字符串是「选择器能不能用」的唯一接口,形状错了在界面上
// 只表现为「点了没反应」;Windows 路径的反斜杠必须经 JSON 转义。
fn pickJson(done: bool, path: Option<&str>) -> String {
    if !done {
        return "{\"status\":\"pending\"}".to_string();
    }
    match path {
        Some(p) => {
            let lit = serde_json::to_string(p).unwrap_or_else(|_| "\"\"".to_string());
            format!("{{\"status\":\"done\",\"path\":{lit}}}")
        }
        None => "{\"status\":\"cancel\"}".to_string(),
    }
}

// pick_folder_poll 取选择器结果(JSON,避免依赖结构体序列化的边界行为):
//   {"status":"pending"} | {"status":"done","path":"C:\\x"} | {"status":"cancel"}
// 取到终态即复位,下一次 begin 从干净状态开始。
#[tauri::command]
fn pick_folder_poll(app: AppHandle) -> String {
    let st = app.state::<PickState>();
    let mut g = st.0.lock().unwrap();
    if !g.done {
        return pickJson(false, None);
    }
    let json = pickJson(true, g.path.as_deref());
    *g = PickSlot::default();
    json
}

// autostart_state 查询开机自启的实际状态(设置面板「关于 gah」展示)。
// 托盘勾选态与系统实际状态可能不同步(用户从系统设置里改过),这里直接问系统。
// 设置面板开着时会轮询本命令(1.5 秒一次),故**不写日志**;启动自检的通道矩阵已报过一次。
#[tauri::command]
fn autostart_state(app: AppHandle) -> String {
    if app.autolaunch().is_enabled().unwrap_or(false) {
        "on".to_string()
    } else {
        "off".to_string()
    }
}

// shell_log 让页面把诊断写进壳日志(前缀 web:,与壳侧、sidecar stderr 同一份文件)。
//
// 桌面版没有终端:页面侧的失败(IPC 调用被拒、后端返回错误)在真机上不留任何痕迹,
// 于是「点了没反应」只能靠猜。把失败原因交给这条通道,一次日志就能定位。
#[tauri::command]
fn shell_log(app: AppHandle, msg: String) {
    shellLog(&app, &format!("web: {msg}"));
}

// httpGETAuth 带凭据的最小 GET(token 模式必须带 cookie,否则 401 → 空串)。
fn httpGETAuth(path: &str) -> String {
    let mut s = match TcpStream::connect_timeout(&web_sockaddr(), Duration::from_millis(300)) {
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

// stateRunning 服务是否在运行(/api/state)。
// 复用 httpGETAuth(原先这里有一份重复的手写 socket 代码):读不到/超时 → 空串 → false,
// 与旧实现在「连不上就当没在跑」上语义一致。
fn stateRunning() -> bool {
    httpGETAuth("/api/state").contains("\"running\":true")
}

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

// defaultWorkspace 交给 sidecar 的初始工作目录(None = 不设,沿用壳自己的 cwd)。
//
// 为何需要:Windows 真机日志里 agent 的默认工作区是 C:\WINDOWS\system32 —— 壳被自启/
// 快捷方式拉起时 cwd 由进程管理器给定,于是整个会话的默认落点跑到系统目录上,
// 既不是用户想要的,让 agent 在那儿写文件也不安全。
// 只有「壳自己被人从某个项目目录里手工拉起」才沿用那个 cwd(那是明确的意图),
// 判断依据是 cwd 是否落在系统目录里(Windows 用 %SystemRoot%,类 Unix 用 / 与 /System、/usr)。
fn defaultWorkspace() -> Option<PathBuf> {
    if let Ok(cwd) = std::env::current_dir() {
        let is_system = match std::env::var("SystemRoot").ok().map(PathBuf::from) {
            Some(root) => cwd.starts_with(root),
            None => {
                cwd == std::path::Path::new("/")
                    || cwd.starts_with("/System")
                    || cwd.starts_with("/usr")
            }
        };
        if !is_system {
            return Some(cwd);
        }
    }
    let home = std::env::var("USERPROFILE")
        .ok()
        .or_else(|| std::env::var("HOME").ok())
        .map(PathBuf::from)?;
    home.is_dir().then_some(home)
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

// logLine 按显式路径追加一行(panic 钩子用:钩子函数拿不到 AppHandle)。写失败一律忽略。
fn logLine(path: &std::path::Path, msg: &str) {
    if let Some(d) = path.parent() {
        if std::fs::create_dir_all(d).is_err() {
            return;
        }
    }
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);
    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    {
        use std::io::Write;
        let _ = writeln!(f, "[{ts}] {msg}");
    }
}

// installPanicLog 装 panic 钩子:任何线程 panic 都写进壳日志,然后交回原钩子。
//
// 为何必须:Windows 桌面版是 GUI 子系统,没有终端 —— panic 随进程消失,机器上不留痕迹。
// 更隐蔽的是 async 任务里的 panic:没有响应者,前端 await 永久挂起,用户看到的就是
// 「点了没反应」(2026-09-17 真机:两个入口全部无反应却查无实据)。装了它之后,
// 「任务没被调度」与「任务跑了但崩了」在日志上立刻可分。
fn installPanicLog(path: PathBuf) {
    let _ = LOG_PATH.set(path);
    let prev = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        if let Some(p) = LOG_PATH.get() {
            let loc = info
                .location()
                .map(|l| format!("{}:{}", l.file(), l.line()))
                .unwrap_or_else(|| "(未知位置)".to_string());
            let msg = info
                .payload()
                .downcast_ref::<&str>()
                .map(|s| (*s).to_string())
                .or_else(|| info.payload().downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "(非字符串 panic)".to_string());
            logLine(p, &format!("panic @ {loc}: {msg}"));
        }
        prev(info);
    }));
}

// isTextLogLine 判断一段输出是否像「人读的日志行」而非 go-plugin 的协议帧:
// 无控制字符(除 tab)、不含 UTF-8 解码失败留下的替换字符、长度合理。
fn isTextLogLine(s: &str) -> bool {
    !s.is_empty()
        && s.len() < 2000
        && !s.contains('\u{FFFD}')
        && !s.chars().any(|c| c.is_control() && c != '\t')
}

// shellLog 追加一行壳侧诊断(时间戳 + 内容)。
//
// 为何需要:Windows 桌面版没有终端,stderr 无处可去 —— 壳在 setup 里失败时用户只看到
// 「窗口白闪一下就没了」,而机器上不留下任何痕迹,只能靠读代码猜。2026-09-15 v0.1.3 真机
// 白屏就是这样:代码面全部排查无果、机器上取不到证据。写失败一律忽略。
fn shellLog(app: &AppHandle, msg: &str) {
    logLine(&shellLogPath(app), msg);
}

// shellLogTail 取壳侧日志最后 n 行(诊断面板要把现场直接摆到用户眼前)。
fn shellLogTail(app: &AppHandle, n: usize) -> String {
    let txt = std::fs::read_to_string(shellLogPath(app)).unwrap_or_default();
    let lines: Vec<&str> = txt.lines().collect();
    lines[lines.len().saturating_sub(n)..].join("\n")
}

// esc 最小 HTML 转义:日志原文要摆进页面,`<`/`&` 不能被当成标签。
fn esc(s: &str) -> String {
    s.replace('&', "&amp;").replace('<', "&lt;").replace('>', "&gt;")
}

// showShellDiag 把「出了什么事」直接铺到窗口上,不再把用户留在启动页/白屏里。
// 失败过去只写日志:真机上窗口白着、机器上没痕迹,只能读代码猜(2026-09-15/16 两轮白屏)。
// 面板附壳日志尾部 —— sidecar 的 stderr 现在也落在里面,原因通常就在那几行。
fn showShellDiag(app: &AppHandle, title: &str, lines: &[String]) {
    let body = lines.iter().map(|l| esc(l)).collect::<Vec<_>>().join("<br>");
    let html = format!(
        "<div style=\"font-family:system-ui,-apple-system,Segoe UI,sans-serif;padding:36px;max-width:720px;line-height:1.7\">\
<h2 style=\"margin:0 0 12px\">{}</h2><p>{body}</p>\
<p style=\"color:#666\">日志文件:{}</p>\
<pre style=\"white-space:pre-wrap;background:#f5f5f5;padding:12px;border-radius:6px;font-size:12px\">{}</pre></div>",
        esc(title),
        esc(&shellLogPath(app).display().to_string()),
        esc(&shellLogTail(app, 16)),
    );
    if let (Some(w), Ok(lit)) = (app.get_webview_window("main"), serde_json::to_string(&html)) {
        let _ = w.eval(&format!(
            "document.body.style.background='#fff';document.body.innerHTML={lit}"
        ));
    }
}

// startCheckWatchdog 检查更新的兜底看门狗:75 秒后若那一轮还没有结果,说明 async 任务没回来
// (真机出现过「一直停在检查更新中…」,连 45 秒超时都没触发 ⇒ 超时那层自己也没跑)。
// 兜底三件:写日志(自证) + 解除进行中状态(菜单不再卡死) + 弹框告知用户。
fn startCheckWatchdog(app: &AppHandle, seq: u64) {
    let h = app.clone();
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_secs(75));
        if CHECK_DONE.load(Ordering::SeqCst) >= seq {
            return;
        }
        shellLog(
            &h,
            &format!("托盘: 检查更新第 {seq} 轮 75 秒无结果 —— async 任务没有返回(超时也没触发)"),
        );
        CHECK_DONE.fetch_max(seq, Ordering::SeqCst);
        setCheckBusy(&h, false);
        let o = UpdateOutcome::new(
            "failed",
            None,
            "检查更新在 75 秒内没有任何结果:壳的异步任务没有返回(连 45 秒超时都没触发)。这通常不是网络问题,请把壳日志发给开发者。".into(),
        );
        recordUpdate(seq, &o);
        notifyUpdate(&h, &o);
    });
}

// setCheckBusy 把托盘「检查更新…」切成进行中(文字 + 禁用),并顺手发一条「正在检查更新…」。
// 检查要走网络,网络不通时可能几十秒没任何动静 —— 「点了没反应」是 2026-09-16 连续两轮的真机
// 反馈。此处的两条通道职责不同:通知立刻到手(但系统可能不弹),菜单文字必然可见(但要再点开
// 托盘菜单才看得到)。
fn setCheckBusy(app: &tauri::AppHandle, busy: bool) {
    setUpdateBusy(busy);
    if let Some(item) = app.state::<TrayCheck>().0.lock().unwrap().as_ref() {
        let _ = item.set_text(if busy { CHECK_BUSY_TEXT } else { CHECK_IDLE_TEXT });
        let _ = item.set_enabled(!busy);
    }
    if busy {
        let _ = app.notification().builder().title("gah").body("正在检查更新…").show();
    }
}

// JS_ERRHOOK 尽早装上前端错误捕获。
//
// 为何要装:纯白页最需要的就是「前端到底报了什么错」这一句话,而模块脚本(ESM)是延迟执行的 ——
// 导航后立即注入的监听器通常仍早于 bundle 开跑,于是未捕获异常能被下一轮自检读出来。
// 幂等,可反复注入。
const JS_ERRHOOK: &str = r#"if (!window.__gahErr) { window.__gahErr = [];
  var cap = function (s) { return String(s).slice(0, 300); };
  window.addEventListener('error', function (e) {
    window.__gahErr.push(cap((e.message || e.error || 'error') + ' @ ' + (e.filename || '?') + ':' + (e.lineno || 0))
      + (e.error && e.error.stack ? '\n' + cap(e.error.stack) : ''));
  });
  window.addEventListener('unhandledrejection', function (e) {
    window.__gahErr.push('rejection: ' + cap(e.reason) + (e.reason && e.reason.stack ? '\n' + cap(e.reason.stack) : ''));
  });
}"#;

// JS_SELFCHECK 交给页面的自检脚本,`@@DIAG@@` 会被替换成诊断文本的 JSON 字符串字面量。
//
// 同步返回一份页面自述(href / readyState / #app 子节点数 / typeof __TAURI__),并在
// 「已经在 sidecar 页面上、却连 #app 都没挂上」时延迟 2.5 秒把诊断面板铺满窗口 —— 宁可难看,
// 也不要让用户对着一片白屏只能报「白屏」。
// 判定前先看 hostname:splash 是 tauri://localhost,它本来就没有 #app,不能误伤。
const JS_SELFCHECK: &str = r#"(function () {
  var app = document.getElementById('app');
  var mounted = !!(app && app.childElementCount > 0);
  // 壳能力探测:前端到底能不能调进壳(由 capabilities/ACL 决定)。结果异步,留给下一轮自检读。
  // 这不是摆设 —— 界面上的「检查更新」走的正是这条通道。
  // 用 __TAURI_INTERNALS__(init 脚本总会注入)而非 __TAURI__:后者只在 withGlobalTauri 打开时有。
  var inv = (window.__TAURI_INTERNALS__ && window.__TAURI_INTERNALS__.invoke)
    || (window.__TAURI__ && window.__TAURI__.core && window.__TAURI__.core.invoke);
  if (!window.__gahIpc && inv) {
    window.__gahIpc = 'pending';
    inv('shell_probe').then(function (r) {
      window.__gahIpc = 'ok: ' + String(r);
    }, function (e) {
      window.__gahIpc = 'denied: ' + String(e);
    });
    // 通道矩阵:除同步探针外,异步命令 / 选择器轮询 / 开机自启查询各探一次。
    // 2026-09-17 真机上出现过「async 命令连函数体都没进(日志无入口行)」,
    // 没有这一格就只能靠猜 —— 而这三个命令正是用户点得最多的入口。
    window.__gahChan = {};
    var one = function (name, args) {
      inv(name, args).then(function (r) {
        window.__gahChan[name] = String(r).slice(0, 60);
      }, function (e) {
        window.__gahChan[name] = 'ERR: ' + String(e).slice(0, 80);
      });
    };
    one('probe_async');
    one('pick_folder_poll');
    one('autostart_state');
    one('update_state');
  }
  if (!mounted && location.hostname === '127.0.0.1' && !window.__gahPanel) {
    window.__gahPanel = 1;
    setTimeout(function () {
      var a2 = document.getElementById('app');
      if (a2 && a2.childElementCount > 0) return;
      var pre = document.createElement('pre');
      pre.style.cssText = 'margin:0;padding:24px;min-height:100%;box-sizing:border-box;background:#fff;color:#171a1f;font:12px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace;white-space:pre-wrap';
      pre.textContent = @@DIAG@@ + '\n\n页面自述: ' + location.href
        + '\n  readyState=' + document.readyState
        + '   title=' + document.title
        + '   #app 子节点=' + (a2 ? a2.childElementCount : -1)
        + '   typeof __TAURI__=' + (typeof window.__TAURI__)
        + '\n  前端错误: ' + JSON.stringify(window.__gahErr || [])
        + '\n  前端→壳 IPC: ' + (window.__gahIpc || 'n/a')
        + '\n  通道矩阵: ' + JSON.stringify(window.__gahChan || {})
        + '\n  __TAURI_INTERNALS__.invoke: ' + typeof ((window.__TAURI_INTERNALS__ || {}).invoke)
        + '\n  正文前 200 字: ' + (document.body && document.body.innerText || '').slice(0, 200);
      document.body.innerHTML = '';
      document.body.appendChild(pre);
    }, 2500);
  }
  return JSON.stringify({
    href: location.href,
    ready: document.readyState,
    mounted: mounted,
    appChildren: app ? app.childElementCount : -1,
    bodyLen: document.body ? document.body.innerHTML.length : -1,
    tauri: typeof window.__TAURI__,
    title: document.title,
    ipc: window.__gahIpc || 'n/a',
    chan: window.__gahChan || {},
    errs: window.__gahErr || [],
    text: document.body && document.body.innerText ? document.body.innerText.slice(0, 200) : ''
  });
})()"#;

// selfcheckScript 把诊断文本填进自检脚本。
fn selfcheckScript(diag: &str) -> String {
    let lit = serde_json::to_string(diag).unwrap_or_else(|_| "\"\"".into());
    JS_SELFCHECK.replace("@@DIAG@@", &lit)
}

// spawnSelfCheck 导航后自检(两轮,跨「导航到达前后」两个文档)。
//
// 为何必须做(2026-09-15 Windows 真机白屏):壳里原先没有任何一处会留下痕迹 ——
// `let _ = w.navigate(u)` 吞掉错误、取不到 main 窗口是静默的、WebView2 渲染进程死掉更是无声,
// 于是只能靠读代码猜。而前端页面已在本机用 CDP 实测过能正常挂载(#app 有子节点,注入
// __TAURI__ 桩后也一样),所以问题只可能在壳侧或 WebView2 —— 那就必须让它们说话。
//
// 判据:两轮都拿不到回话 ⇒ 渲染进程不可用(白屏直接成因);回话里 href 仍是 tauri:// ⇒ 导航
// 没生效;href 是 127.0.0.1 而 mounted=false ⇒ 页面送达了却没渲染出来。
fn spawnSelfCheck(h: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let data_root = h
            .state::<DataRoot>()
            .0
            .lock()
            .ok()
            .and_then(|g| g.clone())
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| "?".into());
        let diag = format!(
            "gah {} 桌面壳自检\n主程序: {}\n数据根: {}\n导航目标: {}\n\n壳侧日志(尾部):\n{}",
            h.package_info().version,
            std::env::current_exe()
                .map(|p| p.display().to_string())
                .unwrap_or_else(|_| "?".into()),
            data_root,
            shell_url(),
            shellLogTail(&h, 24),
        );
        let script = selfcheckScript(&diag);
        for round in 0..2 {
            std::thread::sleep(Duration::from_secs(if round == 0 { 3 } else { 4 }));
            let Some(w) = h.get_webview_window("main") else {
                shellLog(&h, "自检: 取不到 label=main 的窗口,界面无法被导航");
                return;
            };
            match w.url() {
                Ok(u) => shellLog(&h, &format!("自检[{round}]: 当前 URL={u}")),
                Err(e) => shellLog(&h, &format!("自检[{round}]: 读 URL 失败: {e}")),
            }
            let done = std::sync::Arc::new(AtomicBool::new(false));
            let (d2, h2) = (done.clone(), h.clone());
            if let Err(e) = w.eval_with_callback(script.clone(), move |v| {
                d2.store(true, Ordering::SeqCst);
                shellLog(&h2, &format!("自检[{round}]: 页面自述 = {v}"));
            }) {
                shellLog(
                    &h,
                    &format!("自检[{round}]: eval 失败({e}) —— WebView2 渲染进程不可用,界面无法渲染"),
                );
                return;
            }
            for _ in 0..16 {
                if done.load(Ordering::SeqCst) {
                    break;
                }
                std::thread::sleep(Duration::from_millis(500));
            }
            if !done.load(Ordering::SeqCst) {
                shellLog(
                    &h,
                    &format!("自检[{round}]: 页面 8 秒内无回话 —— WebView2 渲染进程无响应(白屏直接成因)"),
                );
            }
        }
    });
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
    match stage::stage_sidecar(&src, &home, &app.package_info().version.to_string()) {
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
        // 原生对话框:托盘「关于 gah」/检查更新的结果反馈,以及界面里的文件夹选择器
        // (plugin:dialog|open)都走它 —— 系统通知不一定来,而模态框必然可见。
        .plugin(tauri_plugin_dialog::init())
        .manage(Sidecar(Mutex::new(None)))
        .manage(DataRoot(Mutex::new(None)))
        .manage(TrayAutostart(Mutex::new(None)))
        .manage(TrayCheck(Mutex::new(None)))
        // 文件夹选择器的一次运行状态(begin/poll 三条命令共用)。
        .manage(PickState(Mutex::new(PickSlot::default())))
        // 界面内升级入口:桌面壳此前没有 invoke 通道,升级只能靠托盘菜单,
        // 菜单弹不出来就等于完全没有升级入口(Windows 真机反馈)。
        .invoke_handler(tauri::generate_handler![
            check_update,
            shell_probe,
            probe_async,
            pick_folder_begin,
            pick_folder_poll,
            autostart_state,
            update_state,
            shell_log
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            // 最早装 panic 钩子:此后任何线程的 panic 都会落进壳日志(桌面版没有终端)。
            installPanicLog(shellLogPath(app.handle()));
            shellLog(
                app.handle(),
                &format!(
                    "setup 开始: 版本 {} 主程序 {}",
                    app.package_info().version,
                    std::env::current_exe().map(|p| p.display().to_string()).unwrap_or_else(|_| "?".into())
                ),
            );
            // 本次运行的 Web 地址:挑一个空闲端口。不能沿用固定 2233 —— 那是 `gah web` 的默认
            // 端口,撞上孤儿 sidecar 或用户自己的 web 实例时,壳会连到**别人的**实例,
            // 界面显示的就不是本次进程(真机表现:装了新版却没有新功能)。
            let web = pick_free_port();
            let _ = WEB_ADDR.set(web.clone());
            shellLog(app.handle(), &format!("本次 Web 地址: http://{web}"));
            // —— 托盘:打开窗口 / 开机自启开关 / 退出 ——
            let show_item = MenuItemBuilder::with_id("show", "显示窗口")
                .build(app)
                .unwrap();
            // 开机自启用勾选项而不是普通项:这里本来就是它唯一的「状态显示器」,
            // 旧实现点完既不报错也不变化,用户只能认为「点了没反应」(2026-09-16 真机)。
            let autostart_item = CheckMenuItemBuilder::with_id("autostart", "开机自启")
                .checked(app.autolaunch().is_enabled().unwrap_or(false))
                .build(app)
                .unwrap();
            let check_item = MenuItemBuilder::with_id("check_update", CHECK_IDLE_TEXT)
                .build(app)
                .unwrap();
            let about_item = MenuItemBuilder::with_id("about", "关于 gah")
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
                    &about_item,
                    &PredefinedMenuItem::separator(app).unwrap(),
                    &quit_item,
                ])
                .build()
                .unwrap();
            // 交出所有权供事件回写状态(菜单已在上一步自己留了引用)
            *app.state::<TrayAutostart>().0.lock().unwrap() = Some(autostart_item);
            *app.state::<TrayCheck>().0.lock().unwrap() = Some(check_item);
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
                        let m = app.autolaunch();
                        let before = m.is_enabled().unwrap_or(false);
                        let res = if before { m.disable() } else { m.enable() };
                        // 以**实际状态**回写勾选态(不假设切换一定成功);日志留痕便于真机排查
                        let now = m.is_enabled().unwrap_or(before);
                        shellLog(app, &format!("开机自启:{before} → {now}(err={:?})", res.err()));
                        if let Some(item) = app.state::<TrayAutostart>().0.lock().unwrap().as_ref() {
                            let _ = item.set_checked(now);
                        }
                    }
                    "check_update" => {
                        // 托盘「检查更新」:先给**立刻可见的进行中反馈**,再异步检查(45 秒封顶),
                        // 结果经通知 + 原生对话框反馈。封顶很关键:网络到不了更新端点时,没有超时就是
                        // 无限期「没反应」。
                        // 另加一层看门狗(75 秒):真机上出现过「一直停在检查更新中…」—— 那说明
                        // async 任务根本没回来(超时也没触发),这类「任务没回来」必须自证并兜底。
                        let seq = CHECK_SEQ.fetch_add(1, Ordering::SeqCst) + 1;
                        shellLog(app, &format!("托盘: 检查更新开始(第 {seq} 轮)"));
                        setCheckBusy(app, true);
                        startCheckWatchdog(app, seq);
                        let h = app.clone();
                        tauri::async_runtime::spawn(async move {
                            shellLog(&h, "托盘: 检查更新任务体已开始执行");
                            let t0 = std::time::Instant::now();
                            let outcome = match tokio::time::timeout(
                                Duration::from_secs(45),
                                checkForUpdates(&h),
                            )
                            .await
                            {
                                Ok(o) => o,
                                Err(_) => UpdateOutcome::new(
                                    "failed",
                                    None,
                                    "检查更新超时(45 秒未返回):多半是网络到不了更新端点。可配好代理后重试,或直接到 GitHub Releases 手动下载安装包。".into(),
                                ),
                            };
                            shellLog(
                                &h,
                                &format!(
                                    "托盘: 检查更新结束 status={} 耗时={}ms",
                                    outcome.status,
                                    t0.elapsed().as_millis()
                                ),
                            );
                            CHECK_DONE.fetch_max(seq, Ordering::SeqCst);
                            setCheckBusy(&h, false);
                            recordUpdate(seq, &outcome);
                            notifyUpdate(&h, &outcome);
                        });
                    }
                    "about" => {
                        // 托盘也要能看到版本与落点:窗口缩在托盘里时,用户最先够到的就是这个菜单
                        let root = app
                            .state::<DataRoot>()
                            .0
                            .lock()
                            .ok()
                            .and_then(|g| g.clone())
                            .map(|p| p.display().to_string())
                            .unwrap_or_else(|| "(未就绪)".into());
                        // 开机自启按**系统实际状态**展示(勾选态可能与系统不同步)。
                        let autostart = if app.autolaunch().is_enabled().unwrap_or(false) {
                            "已启用"
                        } else {
                            "未启用"
                        };
                        let txt = format!(
                            "gah 桌面版 {}\n\nWeb 地址:{}\n数据根:{root}\n开机自启:{autostart}\n日志:{}",
                            app.package_info().version,
                            web_url(),
                            shellLogPath(app).display()
                        );
                        let _ = app.dialog().message(txt).title("关于 gah").show(|_| {});
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
            let cmd = if rt.external {
                app.shell().command(&rt.bin)
            } else {
                app.shell().sidecar("gah").expect("externalBin 缺失: 先运行 scripts/gen-desktop.sh")
            }
            .env("GAH_WEB_OPEN", "0")
            // 把本次挑到的端口交给 sidecar(web 侧读 GAH_WEB_ADDR 覆写 data.addr)
            .env("GAH_WEB_ADDR", web_addr())
            // 父死子死:壳被强杀/崩溃时 sidecar 靠 stdin EOF 自己退出(不会留下占着
            // 数据根的孤儿 —— 那正是「升级后界面还是旧的」的成因)
            .env("GAH_WEB_PARENT_WATCH", "1");
            // 初始工作区见 defaultWorkspace(真机上壳的 cwd 是 C:\WINDOWS\system32)
            let cmd = match defaultWorkspace() {
                Some(w) => {
                    shellLog(app.handle(), &format!("sidecar 初始工作目录: {}", w.display()));
                    cmd.current_dir(w)
                }
                None => cmd,
            };
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
                // 这行是「async 运行时到底跑不跑任务」的自证:真机上若日志有「sidecar 已启动」
                // 却没有这一行,说明 async 任务根本没被调度(而不是事件源没数据)。
                shellLog(&handle2, "sidecar 事件循环已启动");
                while let Some(ev) = rx.recv().await {
                    match ev {
                        tauri_plugin_shell::process::CommandEvent::Stderr(line) => {
                            let text = String::from_utf8_lossy(&line).trim().to_string();
                            // 服务端错误(如「附件不可用」400、端口占用)原先只在事件窗口一闪而过,
                            // 落不到真机上能取回的文件里 —— 追加进壳日志,发一个文件就能定位。
                            // 滤掉 go-plugin 的 [DEBUG] 噪音:一次工作区切换就会刷出几十行
                            // (插件启停/RPC 地址),而 showShellDiag 摆给用户看的正是日志尾部,
                            // 噪声会把真正的原因挤出屏幕。INFO/WARN/ERROR 一律保留。
                            for one in text.lines().filter(|l| !l.contains("[DEBUG]")) {
                                shellLog(&handle2, &format!("sidecar: {one}"));
                            }
                            let _ = handle2.emit("sidecar-log", text);
                        }
                        // Stdout 也收,但只兜「看起来是给人读的日志行」:sidecar 的 stdout 同时是
                        // go-plugin 的二进制 RPC 通道,协议帧绝不能倒进日志(会把文件撑爆且不可读)。
                        // 真机上 stderr 一条都没有时,靠这里判断「日志其实被写到了 stdout」。
                        tauri_plugin_shell::process::CommandEvent::Stdout(line) => {
                            let text = String::from_utf8_lossy(&line).trim().to_string();
                            let mut n = STDOUT_LOGGED.load(Ordering::Relaxed);
                            for one in text.lines() {
                                if n >= 200 {
                                    break;
                                }
                                if one.contains("[DEBUG]") || !isTextLogLine(one) {
                                    continue;
                                }
                                n += 1;
                                shellLog(&handle2, &format!("sidecar(out): {one}"));
                            }
                            STDOUT_LOGGED.store(n, Ordering::Relaxed);
                            let _ = handle2.emit("sidecar-log", text);
                        }
                        // sidecar 提前死掉 ⇒ 界面必然停在启动页(用户看到的就是白屏)。以前要干等
                        // 探活超 12 秒才提一句,现在立刻留痕并把原因铺到窗口上。
                        tauri_plugin_shell::process::CommandEvent::Terminated(payload) => {
                            shellLog(&handle2, &format!("sidecar 已退出: {payload:?}"));
                            if !READY.load(Ordering::SeqCst) {
                                showShellDiag(
                                    &handle2,
                                    "gah 服务进程已退出",
                                    &[
                                        "后台服务在界面就绪前就退出了。下方日志里 `sidecar:` 开头的行是它自己的输出。"
                                            .into(),
                                        format!("退出详情: {payload:?}"),
                                    ],
                                );
                            }
                        }
                        _ => {}
                    }
                }
            });

            // —— 轮询就绪 → navigate(端口是本次私有端口,不存在「接管别人实例」) ——
            let handle3 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                for _ in 0..60 {
                    if httpProbe("/api/state", Duration::from_millis(400)) {
                        READY.store(true, Ordering::SeqCst);
                        shellLog(&handle3, "sidecar 探活就绪,准备 navigate");
                        let _ = handle3.emit("sidecar-ready", web_url());
                        match shell_url().parse() {
                            Ok(u) => {
                                // 导航失败必须留痕:此处原本是 `let _ = w.navigate(u)`,错误被吞掉,
                                // 真机白屏时根本无从判断「到底有没有导航过去」。
                                match handle3.get_webview_window("main") {
                                    Some(w) => match w.navigate(u) {
                                        Ok(()) => shellLog(&handle3, "导航已受理"),
                                        Err(e) => shellLog(&handle3, &format!("navigate 失败: {e}")),
                                    },
                                    None => shellLog(
                                        &handle3,
                                        "致命: 取不到 label=main 的窗口,界面无法被导航",
                                    ),
                                }
                                // 抢在 bundle 之前装错误捕获:ESM 是延迟执行的,而导航刚受理时注入很可能
                                // 仍落在旧文档(splash)上 —— 所以密集小剂量重试,命中新文档的那一次就生效。
                                {
                                    let h = handle3.clone();
                                    tauri::async_runtime::spawn(async move {
                                        for _ in 0..30 {
                                            if let Some(w) = h.get_webview_window("main") {
                                                let _ = w.eval(JS_ERRHOOK);
                                            }
                                            std::thread::sleep(Duration::from_millis(150));
                                        }
                                    });
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
                                // 导航后的自检:把「页面到底挂上了没」写进日志,并在真没挂上时
                                // 把诊断面板铺到窗口上(见 spawnSelfCheck 说明)。
                                spawnSelfCheck(handle3.clone());
                            }
                            Err(e) => {
                                let _ = handle3.emit("sidecar-start-failed", format!("访问地址解析失败: {e}"));
                            }
                        }
                        return;
                    }
                    std::thread::sleep(Duration::from_millis(200));
                }
                shellLog(&handle3, "sidecar 在 12 秒内未就绪(注入失败页)");
                let _ = handle3.emit("sidecar-start-failed", "gah 12 秒内未就绪");
                let root = handle3
                    .state::<DataRoot>()
                    .0
                    .lock()
                    .ok()
                    .and_then(|g| g.clone())
                    .map(|p| p.display().to_string())
                    .unwrap_or_else(|| "gah-data".into());
                showShellDiag(
                    &handle3,
                    "gah 服务未能启动",
                    &[
                        format!("数据根: {root}(需可写)。"),
                        "下方是壳日志尾部:`sidecar:` 开头的行是后台服务自己的输出。".into(),
                        "若 config/bundle-web.yaml 的 data.auth_token 非空(token 模式),桌面壳需以 GAH_WEB_TOKEN 传入同一 token,否则 /api/* 一律 401。"
                            .into(),
                    ],
                );
            });

            // —— 提示流 + 回合结束:一条轮询、两个信号源(NOND-N2) ——
            //
            // 取代原先两条各睡各的循环(2s 轮询 state.running 翻转 + 5s 轮询 /api/schedules
            // 挑 failed)。判据不再复制到壳里:壳只把宿主下发的提示(warn/error)转成系统通知,
            // 于是新类别(后台任务终态、回合报错)自动进来,不必再改壳。
            // 回合结束仍在壳侧 —— 宿主**不发**这类提示(它不是"需要人回来的时刻"),
            // 故保留 running→idle 翻转检测,与提示流共用同一条 2s 轮询。
            let handle4 = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                let mut consumer = notice::Consumer::new();
                let mut prev = stateRunning();
                loop {
                    std::thread::sleep(Duration::from_secs(2));
                    if !READY.load(Ordering::SeqCst) {
                        continue;
                    }
                    // 1) 宿主提示(与 Web toast / TUI 状态栏同一事实源):只 warn/error 弹通知
                    let path = format!("/api/notices?since={}", consumer.since());
                    if let Some(feed) = notice::parseFeed(&httpGETAuth(&path)) {
                        for n in consumer.accept(&feed) {
                            let _ = handle4
                                .notification()
                                .builder()
                                .title(notice::notifyTitle(&n))
                                .body(notice::notifyBody(&n))
                                .show();
                        }
                    }
                    // 2) 回合结束(壳侧独有信号)
                    let cur = stateRunning();
                    if prev && !cur {
                        let _ = handle4.notification().builder().title("gah").body("回合已完成").show();
                    }
                    prev = cur;
                }
            });

            // —— 升级冒烟缝(默认关:只有 GAH_SHELL_UPDATE_AUTOCHECK 设了才动) ——
            spawnAutoCheck(app.handle().clone());

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
                    // 无条件回收:正常退出本该由 /api/shutdown 让 sidecar 自杀,但壳崩溃或被强退
                    // 时子进程会变孤儿 —— 真机(白屏那次)就留下了占着 2233 的旧实例,后遗症是
                    // 「升级了但界面还是旧的」。已退出的进程再 kill 一次无害。
                    let _ = c.kill();
                }
            }
        });
}

// quitApp 托盘退出:POST /api/shutdown → 等端口释放(5s) → 仍活 SIGKILL 兜底 → 壳退出。
fn quitApp(app: &AppHandle) {
    let app = app.clone();
    let _ = std::thread::spawn(move || {
        let target = format!("{}/api/shutdown", web_url());
        // 用 shell 插件 child 无关的最小 HTTP POST(TcpStream 手写)
        if let Ok(mut s) = TcpStream::connect_timeout(&web_sockaddr(), Duration::from_millis(500)) {
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

    // 检查更新状态的 JSON 形状是托盘与设置面板两个视图的对齐接口。
    #[test]
    fn update_json_carries_busy_seq_and_escaped_message() {
        let mut s = UpdateSnapshot {
            busy: true,
            seq: 3,
            status: "",
            message: String::new(),
            version: None,
        };
        assert_eq!(
            updateJson(&s),
            "{\"busy\":true,\"seq\":3,\"status\":\"\",\"message\":\"\",\"version\":null}"
        );
        s.busy = false;
        s.status = "installed";
        s.message = "更新 0.1.4 已安装,换行\n与引号\"都在".into();
        s.version = Some("0.1.4".into());
        let j = updateJson(&s);
        assert!(j.contains("\"busy\":false"), "{j}");
        assert!(j.contains("\"version\":\"0.1.4\""), "{j}");
        assert!(j.contains("\\n"), "换行必须转义:{j}");
        assert!(j.contains("\\\""), "引号必须转义:{j}");
        // 编码出来的必须能被 JSON 解析回去(前端 JSON.parse 的硬要求)
        let back: serde_json::Value = serde_json::from_str(&j).expect("合法 JSON");
        assert_eq!(back["seq"], 3);
        assert_eq!(back["status"], "installed");
    }

    // 选择器结果 JSON 的形状是前后端唯一接口:形状错了在界面上只表现为「点了没反应」。
    #[test]
    fn pick_json_shapes_and_windows_escaping() {
        assert_eq!(pickJson(false, None), "{\"status\":\"pending\"}");
        assert_eq!(pickJson(true, None), "{\"status\":\"cancel\"}");
        // Windows 路径的反斜杠必须转义,否则前端 JSON.parse 直接抛(表现为没反应)
        assert_eq!(
            pickJson(true, Some("D:\\work\\我的 项目")),
            "{\"status\":\"done\",\"path\":\"D:\\\\work\\\\我的 项目\"}"
        );
    }
}
