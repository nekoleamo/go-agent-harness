//! 升级源自动切换:在两份 latest.json(Gitee 镜像 / GitHub Release)之间选路。
//!
//! 为什么需要这一层:endpoint 写在编译产物里,单源在国内或国外必有一边难受。两份表的
//! 内容**不同** —— 各自把自己那份安装包的下载地址写进去(Gitee 表指向 Gitee 直链,GitHub
//! 表指向加速前缀),所以「选中哪份表」就等于「从哪个源下载」,一次选择同时解决取表与下载
//! 两跳。
//!
//! 切换分两层:
//!   1. **端点回退**:默认 Gitee 优先(国内直连最快),把两个端点按顺序交给 updater。
//!      Tauri 的 `Updater::check()` 内部就是 `for url in &self.endpoints`,取不到表就试下一个
//!      —— 端点不可达时自动落到另一个源,不需要我们自己探测。
//!   2. **失败记忆**:某个源"表能取到、包却下不下来"(国内直连 GitHub 资产的典型症状)时把它
//!      记下来垫到最后,下次检查先试另一个源。这是第一层覆盖不到的那半段路。
//!
//! 为什么不做主动测速:那要引入 HTTP 客户端依赖(编译体积与依赖下载都要付账),而收益只是
//! 让海外用户从"可用但慢"变成"快"。真要加,在 `ordered()` 前面插一层探测即可,形状已留好。

use std::sync::Mutex;

use tauri::Url;

/// 一个升级源:名称(日志与失败记忆用)与它的 latest.json 地址。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Source {
    pub name: &'static str,
    pub endpoint: &'static str,
}

/// Gitee 镜像仓库里的表(走 raw 通道:匿名可读、不需要 token、不受 tag 变化影响;
/// 该文件由发布流程随代码快照一起提交,CDN 缓存 60 秒)。
pub const GITEE: Source = Source {
    name: "gitee",
    endpoint: "https://gitee.com/null_593_5354/go-agent-harness/raw/master/latest.json",
};

/// GitHub Release 上的表(带 latest 语义;表里的下载地址指向加速前缀,国内外都取得到)。
pub const GITHUB: Source = Source {
    name: "github",
    endpoint: "https://github.com/nekoleamo/go-agent-harness/releases/latest/download/latest.json",
};

/// 固定两个源:国内直连一个,全球可达一个。加源就在这儿加(`ordered` 与单测会自动跟上)。
pub const SOURCES: [Source; 2] = [GITEE, GITHUB];

/// 上次失败的源名。只活在壳进程内存里:下次启动重新从默认顺序开始,不影响正确性。
static LAST_FAILED: Mutex<Option<&'static str>> = Mutex::new(None);

/// 端点顺序:默认按 SOURCES 顺序(Gitee 优先),被记下的失败源垫到最后。
pub fn ordered(last_failed: Option<&str>) -> Vec<Source> {
    let mut list = SOURCES.to_vec();
    if let Some(name) = last_failed {
        // 稳定排序:被记住的源(true=1)排后,其余保持原顺序。
        list.sort_by_key(|s| s.name == name);
    }
    list
}

/// 记一次失败:下次把该源排到最后。
pub fn note_failure(name: Option<&'static str>) {
    if let Some(n) = name {
        *LAST_FAILED.lock().unwrap() = Some(n);
    }
}

/// 记一次成功:清掉失败记忆,恢复默认优先(Gitee 直连)。
pub fn note_success() {
    *LAST_FAILED.lock().unwrap() = None;
}

pub fn last_failed() -> Option<&'static str> {
    *LAST_FAILED.lock().unwrap()
}

/// 本次检查更新要用的端点顺序 + 首选源名(失败时用它记账)。
pub fn select() -> (Vec<Url>, Option<&'static str>) {
    let list = ordered(last_failed());
    let primary = list.first().map(|s| s.name);
    let urls = list
        .iter()
        .filter_map(|s| Url::parse(s.endpoint).ok())
        .collect();
    (urls, primary)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gitee_is_preferred_by_default() {
        let list = ordered(None);
        assert_eq!(list[0].name, "gitee", "默认国内直连优先:{list:?}");
        assert_eq!(list[1].name, "github");
    }

    #[test]
    fn last_failed_source_goes_last() {
        let list = ordered(Some("gitee"));
        assert_eq!(list[0].name, "github", "失败源垫底:{list:?}");
        assert_eq!(list[1].name, "gitee");
        // 记住 GitHub 时对称成立
        assert_eq!(ordered(Some("github"))[0].name, "gitee");
    }

    #[test]
    fn select_returns_parseable_endpoints_in_order() {
        note_success();
        let (urls, primary) = select();
        assert_eq!(urls.len(), SOURCES.len(), "端点不能丢:{urls:?}");
        assert!(urls[0].as_str().contains("gitee.com"));
        assert_eq!(primary, Some("gitee"));
    }

    #[test]
    fn failure_memory_round_trip() {
        note_success();
        assert_eq!(last_failed(), None);
        note_failure(Some("gitee"));
        assert_eq!(last_failed(), Some("gitee"));
        note_failure(None); // 没有首选源时不该清掉已有记忆
        assert_eq!(last_failed(), Some("gitee"));
        note_success();
        assert_eq!(last_failed(), None);
    }

    #[test]
    fn endpoints_are_valid_urls() {
        for s in SOURCES {
            assert!(
                Url::parse(s.endpoint).is_ok(),
                "端点必须是合法 URL:{}",
                s.endpoint
            );
        }
    }
}
