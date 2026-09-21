// NOND-N2(桌面壳侧):桌面壳消费宿主**提示流** `/api/notices`,取代壳内自建的两条轮询。
//
// 为什么换:壳原先自己轮询 `/api/schedules` 挑 `last_status == failed`(见 git 历史里的
// newScheduleFailures)—— 那是把「哪件事值得打扰人」的判据**复制**到了壳里,规则一改就得
// 三端同步改(Web toast / TUI 状态栏 / 桌面通知)。现在判据只在宿主一处(host-notices),
// 壳只负责把它转成系统通知,于是壳自动获得新类别(后台任务终态、回合报错)而不必再改代码。
//
// 壳侧仍然独有的一件事:**回合结束**信号(状态栏 running→idle 翻转)。宿主**不发**这类
// 提示(它面向「需要人回来的时刻」,回合正常结束不是),所以这条仍在壳里,与提示流共用
// 同一条定时轮询 —— 一次 sleep、两个信号源,而不是两条各睡各的循环。
//
// 语义纪律(与 Web/TUI 同口径):
//   - 只有 warn/error 弹系统通知(info 只该刷状态栏,桌面壳没有状态栏 → 丢弃);
//   - 首次成功拉取只**定位游标**、不补发历史(启动前就存在的失败不是新闻);
//   - 游标只增不减;服务端环形缓冲丢弃过条目(gap)必须显式说一次,不静默漏掉。

use serde_json::Value;

/// Notice 宿主下发的一条提示(sdk.Notice 的 JSON 形状)。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Notice {
    pub id: u64,
    pub level: String,
    pub title: String,
    pub body: String,
    pub source: String,
}

/// Feed 一次 `/api/notices?since=N` 的应答。
#[derive(Debug, Clone, Default)]
pub struct Feed {
    pub items: Vec<Notice>,
    pub max_id: u64,
    /// 服务端环形缓冲已丢弃更早条目(期间可能有提示没送到)。
    pub gap: bool,
    /// 被去重(同 Key 窗口内重复)的条数。
    pub suppressed: u64,
}

/// parseFeed 解析 `/api/notices` 正文。`None` = 不是合法 JSON/形状不对
/// (未装配 503、网络半截响应都走这里):调用方据此**不下任何结论**,不当作"没有提示"。
pub fn parseFeed(body: &str) -> Option<Feed> {
    let v: Value = serde_json::from_str(body).ok()?;
    let obj = v.as_object()?;
    let mut feed = Feed {
        max_id: obj.get("max_id").and_then(|x| x.as_u64()).unwrap_or(0),
        gap: obj.get("gap").and_then(|x| x.as_bool()).unwrap_or(false),
        suppressed: obj.get("suppressed").and_then(|x| x.as_u64()).unwrap_or(0),
        items: Vec::new(),
    };
    if let Some(arr) = obj.get("items").and_then(|x| x.as_array()) {
        for it in arr {
            let id = it.get("id").and_then(|x| x.as_u64()).unwrap_or(0);
            if id == 0 {
                continue; // 无 id 的条目无法去重也无法推进游标:丢弃(宿主不会下发这种)
            }
            feed.items.push(Notice {
                id,
                level: str_of(it, "level"),
                title: str_of(it, "title"),
                body: str_of(it, "body"),
                source: str_of(it, "source"),
            });
            if id > feed.max_id {
                feed.max_id = id; // 服务端没给 max_id 时也要能推进
            }
        }
    }
    Some(feed)
}

fn str_of(v: &Value, key: &str) -> String {
    v.get(key).and_then(|x| x.as_str()).unwrap_or("").to_string()
}

/// shouldNotify 是否值得弹系统通知:与 TUI 的 `notifyLevelAllows` 同口径(只 warn/error)。
pub fn shouldNotify(n: &Notice) -> bool {
    matches!(n.level.as_str(), "warn" | "error")
}

/// notifyTitle 系统通知标题(带类别前缀:托盘里一眼能分清是"计划失败"还是"回合报错")。
pub fn notifyTitle(n: &Notice) -> String {
    // 前缀匹配而非精确相等:宿主侧 source 是"谁发的",且**真实值是带 host- 前缀的插件名**
    // (host-schedule / host-jobs / host-agent-loop,见 fixtures/notices-feed.json 真产物)。
    // 故先剥 `host-` 再比,否则 host-jobs/host-schedule 会掉进"提示"这个没信息量的兜底
    // (2026-09-21 真产物喂壳时逮到:此前只按 schedule/job 裸名写,漏了真实前缀)。
    let src = n.source.trim().trim_start_matches("host-");
    let kind = if src.starts_with("schedule") || src.starts_with("cron") {
        "定时任务"
    } else if src.starts_with("job") {
        "后台任务"
    } else if src.starts_with("agent") {
        "回合"
    } else {
        "提示"
    };
    let head = n.title.trim();
    if head.is_empty() {
        return format!("gah {kind}");
    }
    format!("gah {kind}:{head}")
}

/// notifyBody 系统通知正文(空正文 = 不带空行;超长截断并留省略号)。
pub fn notifyBody(n: &Notice) -> String {
    let b = summarize(n.body.trim(), 300);
    if b.is_empty() {
        "详情见会话记录。".to_string()
    } else {
        format!("{b}\n详情见会话记录。")
    }
}

/// summarize 摘要(字符级截断,附省略号;避免系统通知里塞整段报错)。
pub fn summarize(s: &str, max: usize) -> String {
    let t = s.trim();
    if t.chars().count() <= max {
        return t.to_string();
    }
    let head: String = t.chars().take(max).collect();
    format!("{head}…")
}

/// Consumer 提示流游标(状态机,便于单测):
/// 首次 `accept` 只定位游标不补发;其后只放行 id > since 且值得打扰的条目;
/// 游标单调不减(重复/乱序应答不得把它拉回去);服务端丢过条目时补一条显式说明。
#[derive(Debug, Default)]
pub struct Consumer {
    since: u64,
    primed: bool,
    gap_reported: bool,
}

impl Consumer {
    pub fn new() -> Self {
        Self::default()
    }

    /// since 当前游标(下一轮请求用)。
    pub fn since(&self) -> u64 {
        self.since
    }

    /// accept 喂一次应答,返回**应当弹系统通知**的条目(按 id 升序)。
    pub fn accept(&mut self, feed: &Feed) -> Vec<Notice> {
        if !self.primed {
            // 启动前就存在的提示不是新闻:只定位游标(distinct from "没收到")
            self.primed = true;
            self.since = feed.max_id;
            self.gap_reported = feed.gap;
            return Vec::new();
        }
        let mut out: Vec<Notice> = feed
            .items
            .iter()
            .filter(|n| n.id > self.since && shouldNotify(n))
            .cloned()
            .collect();
        out.sort_by_key(|n| n.id);
        if feed.max_id > self.since {
            self.since = feed.max_id;
        }
        // 环状缓冲丢过条目 = 可能有提示没送到:说一次,别让人以为"没通知就是没事"
        if feed.gap && !self.gap_reported {
            self.gap_reported = true;
            out.push(Notice {
                id: self.since,
                level: "warn".into(),
                title: "提示有丢弃".into(),
                body: format!(
                    "服务端提示缓冲已丢弃更早的条目(缓冲上限),期间可能有通知未送达;已去重 {} 条。",
                    feed.suppressed
                ),
                source: "shell".into(),
            });
        } else if !feed.gap {
            self.gap_reported = false;
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const SAMPLE: &str = r#"{"items":[
      {"id":7,"level":"error","title":"计划「每日备份」执行失败","body":"模型调用超时","source":"schedule","ts":"2026-09-18T09:00:00Z"},
      {"id":8,"level":"info","title":"回合已完成","body":"","source":"agent"},
      {"id":9,"level":"warn","title":"后台任务失败","body":"exit 1","source":"job"}
    ],"max_id":9,"gap":false,"suppressed":2}"#;

    /// 宿主真实序列化产物(sdk.NoticePage 经 encoding/json 输出,2026-09-18 采自本仓 sdk 包;
    /// 注意 gap 为 false 时被 omitempty 省略 —— 解析侧必须容忍缺字段)。
    const REAL_SAMPLE: &str = r#"{"items":[{"id":7,"level":"error","title":"计划「每日备份」执行失败","body":"模型调用超时: dial tcp 10.0.0.1:443: i/o timeout","source":"schedule","ts":"2026-09-18T09:30:00Z","key":"schedule:sched-a:failed"},{"id":9,"level":"warn","title":"后台任务失败","body":"exit status 1","source":"job","ts":"2026-09-18T09:30:00Z"}],"max_id":9,"suppressed":2}"#;

    #[test]
    fn real_host_payload_parses() {
        // 这一条是 Rust 侧与 Go 侧字段名的**契约**:宿主改字段名/改形状就要在这里红
        let f = parseFeed(REAL_SAMPLE).expect("宿主真实产物必须能解析");
        assert_eq!(f.max_id, 9, "缺 gap/多余 ts·key 都不该影响解析");
        assert!(!f.gap);
        assert_eq!(f.suppressed, 2);
        assert_eq!(f.items.len(), 2);
        assert_eq!(f.items[0].source, "schedule");
        assert!(shouldNotify(&f.items[0]) && shouldNotify(&f.items[1]));
        assert_eq!(notifyTitle(&f.items[0]), "gah 定时任务:计划「每日备份」执行失败");
    }

    /// 宿主**真产物**驱动壳的判定链:fixture 由 Go 侧真事件总线生产
    /// (`GAH_UPDATE_NOTICE_FIXTURE=1 go test ./tests/ -run TestNoticeFeedFixture`),
    /// 宿主改字段/改形状/改 source 命名 → 这里红。
    #[test]
    fn host_generated_feed_drives_notification_chain() {
        let raw = std::fs::read_to_string(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/fixtures/notices-feed.json"
        ))
        .expect("真产物 fixture 应由 Go 侧生成并入库");
        let f = parseFeed(&raw).expect("宿主真产物必须能解析");
        assert_eq!(f.max_id, 3, "三条真事件各生产一条提示");
        assert_eq!(f.items.len(), 3);
        let sources: Vec<&str> = f.items.iter().map(|n| n.source.as_str()).collect();
        assert_eq!(
            sources,
            vec!["host-jobs", "host-schedule", "host-agent-loop"],
            "来源标识符即壳分类依据,变了就要同步 notifyTitle"
        );
        for n in &f.items {
            assert!(shouldNotify(n), "真产物全是 warn/error:{}", n.level);
        }
        // 类别前缀:host-* 必须都被识别(此前 host-jobs/host-schedule 掉进"提示")
        assert!(notifyTitle(&f.items[0]).starts_with("gah 后台任务:后台任务失败"));
        assert!(notifyTitle(&f.items[1]).starts_with("gah 定时任务:计划「plan-1」本轮跳过"));
        assert!(notifyTitle(&f.items[2]).starts_with("gah 回合:回合出错"));
        // 正文:真产物正文非空 → 附"详情见会话记录。"
        assert!(notifyBody(&f.items[1]).ends_with("详情见会话记录。"));
        assert!(notifyBody(&f.items[1]).contains("到点时有回合在跑"));
        // 游标:首次 accept 只定位不补发,其后才放行(真产物含 3 条也不该在首连时炸一屏)
        let mut c = Consumer::default();
        assert!(c.accept(&f).is_empty(), "首次应答只定位游标");
    }

    #[test]
    fn parse_feed_reads_items_and_meta() {
        let f = parseFeed(SAMPLE).expect("合法 JSON");
        assert_eq!(f.max_id, 9);
        assert_eq!(f.items.len(), 3);
        assert_eq!(f.items[0].id, 7);
        assert_eq!(f.items[0].level, "error");
        assert_eq!(f.items[0].source, "schedule");
        assert_eq!(f.suppressed, 2);
        assert!(!f.gap);
    }

    #[test]
    fn parse_feed_rejects_non_json_without_pretending_empty() {
        // 未装配(503 文本)/半截响应 → None:调用方不得据此断定"没有提示"
        assert!(parseFeed("").is_none());
        assert!(parseFeed("提示通道未装配").is_none());
        assert!(parseFeed("[1,2,3]").is_none());
        // 合法但空:是"确实没有",不是解析失败
        let f = parseFeed(r#"{"items":[],"max_id":5}"#).expect("空列表仍是合法应答");
        assert!(f.items.is_empty());
        assert_eq!(f.max_id, 5);
    }

    #[test]
    fn parse_feed_advances_cursor_without_max_id() {
        let f = parseFeed(r#"{"items":[{"id":3,"level":"warn","title":"x"}]}"#).expect("合法");
        assert_eq!(f.max_id, 3, "服务端没给 max_id 也要能推进游标");
    }

    #[test]
    fn first_pass_only_primes_the_cursor() {
        let f = parseFeed(SAMPLE).unwrap();
        let mut c = Consumer::new();
        assert!(c.accept(&f).is_empty(), "启动前的旧提示不补发");
        assert_eq!(c.since(), 9, "但游标要定位到最新");
    }

    #[test]
    fn only_warn_and_error_notify_after_priming() {
        let mut c = Consumer::new();
        c.accept(&parseFeed(SAMPLE).unwrap());
        let next = parseFeed(
            r#"{"items":[
              {"id":8,"level":"info","title":"回放","source":"agent"},
              {"id":10,"level":"info","title":"只是状态","source":"job"},
              {"id":11,"level":"error","title":"新失败","source":"schedule"}
            ],"max_id":11}"#,
        )
        .unwrap();
        let got = c.accept(&next);
        assert_eq!(got.len(), 1, "info 不打扰,重复 id 不重发");
        assert_eq!(got[0].id, 11);
        assert_eq!(c.since(), 11);
    }

    #[test]
    fn cursor_never_goes_backwards() {
        let mut c = Consumer::new();
        c.accept(&parseFeed(SAMPLE).unwrap());
        // 乱序/重复应答(更小的 max_id)不得把游标拉回去,否则旧提示会重弹
        let stale = parseFeed(r#"{"items":[{"id":3,"level":"error","title":"旧"}],"max_id":3}"#).unwrap();
        assert!(c.accept(&stale).is_empty());
        assert_eq!(c.since(), 9);
    }

    #[test]
    fn gap_is_reported_once() {
        let mut c = Consumer::new();
        c.accept(&parseFeed(SAMPLE).unwrap());
        let gapped = parseFeed(r#"{"items":[{"id":20,"level":"error","title":"新"}],"max_id":20,"gap":true,"suppressed":4}"#).unwrap();
        let got = c.accept(&gapped);
        assert_eq!(got.len(), 2, "丢弃说明与新提示一起给");
        assert!(got.iter().any(|n| n.title.contains("丢弃")), "{got:?}");
        assert!(got[1].body.contains("未送达"), "说明要讲清后果:{got:?}");
        // 持续 gap 不重复刷屏(同一件事说一次)
        let again = parseFeed(r#"{"items":[],"max_id":21,"gap":true}"#).unwrap();
        assert!(c.accept(&again).is_empty());
    }

    #[test]
    fn real_source_names_map_to_readable_kinds() {
        let mk = |source: &str| Notice { id: 1, level: "error".into(), title: "t".into(), body: String::new(), source: source.into() };
        assert_eq!(notifyTitle(&mk("host-agent-loop")), "gah 回合:t");
        assert_eq!(notifyTitle(&mk("schedule")), "gah 定时任务:t");
        assert_eq!(notifyTitle(&mk("schedule-run")), "gah 定时任务:t");
        assert_eq!(notifyTitle(&mk("jobs")), "gah 后台任务:t");
        assert_eq!(notifyTitle(&mk("whatever")), "gah 提示:t");
    }

    /// 真机冒烟(默认跳过):对**活着的** gah web 实例拉一次 /api/notices。
    ///
    ///     GAH_SHELL_NOTICE_ADDR=127.0.0.1:2233 cargo test --offline live_notice
    ///
    /// 验的是 Rust 侧解析/分类与真宿主字节的契约(不是 fixture 自说自话)。
    #[test]
    fn live_notice_stream_smoke() {
        let Ok(addr) = std::env::var("GAH_SHELL_NOTICE_ADDR") else {
            return;
        };
        let _ = super::super::WEB_ADDR.set(addr);
        let body = super::super::httpGETAuth("/api/notices?since=0");
        let feed = parseFeed(&body).expect("活实例应答必须能解析(否则壳与宿主字段名已漂移)");
        let mut c = Consumer::new();
        assert!(c.accept(&feed).is_empty(), "首次只定位游标");
        assert_eq!(c.since(), feed.max_id);
        for n in &feed.items {
            println!("level={} title={} body={}", n.level, notifyTitle(n), notifyBody(n));
        }
    }

    #[test]
    fn titles_and_bodies_are_labelled() {
        let f = parseFeed(SAMPLE).unwrap();
        assert_eq!(notifyTitle(&f.items[0]), "gah 定时任务:计划「每日备份」执行失败");
        assert_eq!(notifyTitle(&f.items[2]), "gah 后台任务:后台任务失败");
        assert!(notifyBody(&f.items[0]).starts_with("模型调用超时"));
        assert!(notifyBody(&f.items[1]).starts_with("详情见会话记录。"), "空正文不留空行");
        let long = Notice {
            id: 1,
            level: "error".into(),
            title: "t".into(),
            body: "错".repeat(400),
            source: "agent".into(),
        };
        assert_eq!(notifyBody(&long).lines().next().unwrap().chars().count(), 301);
        assert_eq!(
            notifyTitle(&Notice { id: 1, level: "warn".into(), title: "  ".into(), body: String::new(), source: "x".into() }),
            "gah 提示",
            "空标题不该拼出「gah 提示:gah」"
        );
    }
}
