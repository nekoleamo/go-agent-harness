// stage.rs 运行态外置(NOND-W2b-α):让用户数据不在应用目录里。
//
// 问题:便携纪律(不变)规定数据根 = **二进制同级** `gah-data/`,单一解析链、不读 env/不传参。
// 但桌面壳里 sidecar 位于 `.app/Contents/MacOS/gah`(mac)或 NSIS 安装目录(win),于是
// 「二进制同级」= 应用目录内 → 升级整包替换会带走数据、卸载(NSIS 删安装目录)会删数据。
//
// 解法:壳先把 sidecar **复制到用户数据目录**,再从那里 spawn。二进制落在用户目录 →
// 同级 `gah-data/` 自然在应用目录之外,而数据根解析规则**一个字没改**:
//
//   mac   ~/Library/Application Support/dev.gah.desktop/bin/{gah-*, gah-data/}
//   win   %LOCALAPPDATA%\dev.gah.desktop\bin\{gah-*, gah-data\}
//
// 注意区别:这**不是**「壳告诉 gah 去哪写数据」(那是 2026-09-16 明确移除的 appDataHome 方案,
// 会破唯一解析链),而是「把二进制搬到用户可写目录,让既有规则自己得出结论」。
//
// 复制时机:副本文件名 = `gah-<版本>-<内容指纹>`(指纹 = 源文件大小 + 源 mtime)。名字不同即重放
// (升级、重新构建);名字相同即复用 —— 因为按内容寻址,**永远不需要覆盖已存在的文件**。
// 这一条很关键:Windows **不允许覆盖正在运行的 exe**,旧版 gah 只要还在跑(驻留托盘,或用户
// 自己开着 `gah --profile web`),固定名 + 覆盖就必然得到「拒绝访问 (os error 5)」,整套外置
// 随之失效、数据退回应用目录内(v0.1.3 真机实证)。
// 落盘是原子的(临时文件 → rename,且目标名必然不存在),失败返回 Err(调用方告警后回退到
// 应用目录内运行,绝不静默降级)。

use std::path::{Path, PathBuf};

/// exe_name sidecar 在磁盘上的文件名(Windows 带 .exe)。
pub fn exe_name() -> &'static str {
    if cfg!(windows) {
        "gah.exe"
    } else {
        "gah"
    }
}

/// bundled_sidecar 随包 sidecar 的路径(externalBin 就在主程序同级)。
pub fn bundled_sidecar() -> Result<PathBuf, String> {
    let exe = std::env::current_exe().map_err(|e| format!("取主程序路径失败: {e}"))?;
    let dir = exe.parent().ok_or("主程序路径无父目录")?;
    let p = dir.join(exe_name());
    if !p.exists() {
        return Err(format!("随包 sidecar 不存在: {}", p.display()));
    }
    Ok(p)
}

/// fnv1a 64 位(5 行自实现):给外置副本的文件名做内容指纹,不为一个哈希引依赖。
fn fnv1a(s: &str) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in s.as_bytes() {
        h ^= u64::from(*b);
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    h
}

/// staged_file_name 外置副本的文件名 = `gah-<版本>-<内容指纹8>[.exe]`。
fn staged_file_name(version: &str, fingerprint: &str) -> String {
    let h = fnv1a(fingerprint) & 0xffff_ffff;
    if cfg!(windows) {
        format!("gah-{version}-{h:08x}.exe")
    } else {
        format!("gah-{version}-{h:08x}")
    }
}

/// staged_bin 外置副本的路径(`<home>/bin/gah-<版本>-<指纹>[.exe]`)。
/// **数据根是它的同级 `gah-data/`** —— 与副本叫什么名字无关。
fn staged_bin(home: &Path, version: &str, fingerprint: &str) -> PathBuf {
    home.join("bin").join(staged_file_name(version, fingerprint))
}

/// data_root 外置后的数据根 = **二进制同级** `gah-data/`(与 cmd/gah homeDir 同口径)。
pub fn data_root(home: &Path) -> PathBuf {
    home.join("bin").join("gah-data")
}

/// legacy_data_root 旧位置(= 应用目录内)的数据根,用于一次性迁移。
pub fn legacy_data_root() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    Some(exe.parent()?.join("gah-data"))
}

/// fingerprint 副本指纹:版本 + 源文件大小 + 源 mtime(nanos)。返回 (指纹文本, 源大小)。
/// 任一变化即换文件名(开发期同版本重建也生效)。
fn fingerprint(src: &Path, version: &str) -> Result<(String, u64), String> {
    let md = std::fs::metadata(src).map_err(|e| format!("读 {} 失败: {e}", src.display()))?;
    let len = md.len();
    let mtime = md
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    Ok((format!("{version}\n{len}\n{mtime}\n"), len))
}

/// StageOutcome stage_sidecar 的结果。
#[derive(Debug)]
pub struct StageOutcome {
    pub bin: PathBuf,
    /// staged=true 表示本次真的复制了(升级/首次/重建);false = 已是最新,跳过。
    pub staged: bool,
}

/// stage_sidecar 把 sidecar 复制到 `<home>/bin/`(必要时),返回可执行路径。
///
/// 文件名带版本与内容指纹(内容寻址),一次解决三件事:
///  ① **永不需要覆盖已存在的文件** —— Windows 不允许覆盖正在运行的 exe,固定名 + rename
///     覆盖遇到「旧副本还在跑」就必然失败(真机报「拒绝访问 (os error 5)」);
///  ② 同名 ⇒ 同版本 + 同大小 + 同 mtime ⇒ 内容一致,直接复用(省掉一次整包复制);
///  ③ 不再需要 `.gah-stage` 标记文件(名字本身就是指纹),少一个可能失同步的状态。
/// 历史副本由 prune_stale 尽力清理:**删不掉不报错**。
pub fn stage_sidecar(src: &Path, home: &Path, version: &str) -> Result<StageOutcome, String> {
    let bin_dir = home.join("bin");
    std::fs::create_dir_all(&bin_dir).map_err(|e| format!("建 {} 失败: {e}", bin_dir.display()))?;
    let (want, src_len) = fingerprint(src, version)?;
    let dst = staged_bin(home, version, &want);
    prune_stale(&bin_dir, &dst);
    // 同名 + 同大小 ⇒ 就是我们要的那一份(rename 是原子的,不存在半成品)。
    let have_ok = dst.exists() && std::fs::metadata(&dst).map(|m| m.len()).unwrap_or(0) == src_len;
    if have_ok {
        return Ok(StageOutcome { bin: dst, staged: false });
    }
    // 临时名也带上指纹:不会和上一轮崩溃残留的半成品撞名。
    let tmp = bin_dir.join(format!(
        "{}.new",
        dst.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default()
    ));
    if tmp.exists() {
        let _ = std::fs::remove_file(&tmp);
    }
    std::fs::copy(src, &tmp)
        .map_err(|e| format!("复制 {} → {} 失败: {e}", src.display(), tmp.display()))?;
    make_executable(&tmp)?;
    strip_mark_of_transfer(&tmp);
    // 目标名按内容定,此刻必然不存在 ⇒ rename 不可能撞上「正在运行的旧副本」的文件锁。
    std::fs::rename(&tmp, &dst).map_err(|e| format!("放下 {} 失败: {e}", dst.display()))?;
    Ok(StageOutcome { bin: dst, staged: true })
}

/// prune_stale 尽力清掉 `bin/` 里的历史外置副本(旧版本、旧命名、未完成的 `.new`、旧标记)。
///
/// 为何必须清:文件名按内容寻址后每次升级都落一个新文件,不清就会一直堆(sidecar 30–45MB)。
/// **删不掉一律忽略** —— Windows 上「正在运行的旧副本」删不掉是正常现象(它正持有文件锁),
/// 下次启动它已经不在了,自然会被清掉;清理失败绝不该影响本次启动,更不该报错。
fn prune_stale(bin_dir: &Path, keep: &Path) {
    let Ok(rd) = std::fs::read_dir(bin_dir) else {
        return;
    };
    for entry in rd.flatten() {
        let p = entry.path();
        // 目录一律不碰:同级 `gah-data/` 是用户数据,误删后果比留垃圾严重得多。
        if p == keep || p.is_dir() {
            continue;
        }
        let name = entry.file_name().to_string_lossy().to_string();
        let ours = name == "gah"
            || name == "gah.exe"
            || name.starts_with("gah-")
            || name.starts_with(".gah-stage");
        if ours {
            let _ = std::fs::remove_file(&p);
        }
    }
}

/// migrate_legacy 一次性迁移:旧数据在应用目录内且新位置还没有数据时复制过去。
/// **只复制不删除**(旧副本留在原地,最坏情况用户仍能找回)。返回迁移落地路径。
pub fn migrate_legacy(home: &Path) -> Result<Option<PathBuf>, String> {
    match legacy_data_root() {
        Some(p) => migrate_from(&p, home),
        None => Ok(None),
    }
}

/// migrate_from 迁移本体(legacy 显式传入,便于单测)。
pub fn migrate_from(legacy: &Path, home: &Path) -> Result<Option<PathBuf>, String> {
    if !legacy.exists() {
        return Ok(None);
    }
    let target = data_root(home);
    if target.exists() {
        return Ok(None);
    }
    copy_tree(legacy, &target)
        .map_err(|e| format!("迁移 {} → {} 失败: {e}", legacy.display(), target.display()))?;
    Ok(Some(target))
}

/// copy_tree 递归复制目录(标准库,桌面壳不引新依赖;符号链接按文件复制)。
pub fn copy_tree(src: &Path, dst: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let (from, to) = (entry.path(), dst.join(entry.file_name()));
        if entry.file_type()?.is_dir() {
            copy_tree(&from, &to)?;
        } else {
            std::fs::copy(&from, &to)?;
        }
    }
    Ok(())
}

#[cfg(unix)]
fn make_executable(p: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755))
        .map_err(|e| format!("设置可执行位失败: {e}"))
}

#[cfg(not(unix))]
fn make_executable(_p: &Path) -> Result<(), String> {
    Ok(())
}

/// strip_mark_of_transfer 去掉复制过来的「来自网络」标记,免得同一份已被用户放行的程序
/// 换个位置又被拦一次。
/// - mac:`fs::copy` 走 copyfile(COPYFILE_ALL),会把 `com.apple.quarantine` 一起带过来 →
///   用系统 `xattr -d` 清掉(失败只忽略:GUI 还会再问一次,不影响正确性)。
/// - win:`fs::copy` 会复制备用数据流(Zone.Identifier) → 删掉该流(失败忽略)。
fn strip_mark_of_transfer(p: &Path) {
    #[cfg(target_os = "macos")]
    {
        let _ = std::process::Command::new("/usr/bin/xattr")
            .arg("-d")
            .arg("com.apple.quarantine")
            .arg(p)
            .output();
    }
    #[cfg(windows)]
    {
        let ads = format!("{}:Zone.Identifier", p.display());
        let _ = std::fs::remove_file(ads);
    }
    #[cfg(all(not(target_os = "macos"), not(windows)))]
    {
        let _ = p;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_dir(tag: &str) -> PathBuf {
        let base = std::env::temp_dir().join(format!("gah-stage-{}-{}", tag, std::process::id()));
        let _ = std::fs::remove_dir_all(&base);
        std::fs::create_dir_all(&base).unwrap();
        base
    }

    fn fake_sidecar(dir: &Path, content: &[u8]) -> PathBuf {
        let p = dir.join("gah-src");
        std::fs::write(&p, content).unwrap();
        p
    }

    #[test]
    fn data_root_is_sibling_of_staged_bin() {
        let home = PathBuf::from("/tmp/x-home");
        let bin = staged_bin(&home, "1.2.3", "fp");
        assert_eq!(bin.parent().unwrap(), home.join("bin"));
        // 副本名带版本与指纹(内容寻址),平台后缀正确
        assert_eq!(bin.file_name().unwrap().to_string_lossy(), staged_file_name("1.2.3", "fp"));
        let name = bin.file_name().unwrap().to_string_lossy().to_string();
        assert!(name.starts_with("gah-1.2.3-"), "{name}");
        assert_eq!(name.ends_with(".exe"), cfg!(windows), "{name}");
        // 数据根恒为副本同级 gah-data/,与副本叫什么名字无关
        assert_eq!(data_root(&home), home.join("bin").join("gah-data"));
    }

    #[test]
    fn stage_copies_then_skips_when_unchanged() {
        let base = tmp_dir("copy");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");

        let first = stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(first.staged);
        assert_eq!(std::fs::read(&first.bin).unwrap(), b"v1");

        // 同版本同文件 → 跳过(不再复制)
        let second = stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(!second.staged);
        assert_eq!(second.bin, first.bin);
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn stage_recopies_on_version_or_source_change() {
        let base = tmp_dir("recopy");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");
        stage_sidecar(&src, &home, "0.1.0").unwrap();

        // 版本变了 → 重放
        let up = stage_sidecar(&src, &home, "0.2.0").unwrap();
        assert!(up.staged);

        // 同版本但文件变了(开发期重建)→ 重放
        std::thread::sleep(std::time::Duration::from_millis(1100));
        std::fs::write(&src, b"v2-longer").unwrap();
        let rebuilt = stage_sidecar(&src, &home, "0.2.0").unwrap();
        assert!(rebuilt.staged);
        assert_eq!(std::fs::read(&rebuilt.bin).unwrap(), b"v2-longer");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn stage_leaves_no_tmp_and_prunes_old_copies() {
        let base = tmp_dir("atomic");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");
        let bin_dir = home.join("bin");
        let tmp_of = |p: &Path| bin_dir.join(format!("{}.new", p.file_name().unwrap().to_string_lossy()));

        let first = stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(!tmp_of(&first.bin).exists(), "不得留下 .new 半成品");

        // 升级(换版本)→ 新副本落地,旧的被清掉(按内容命名不会自己覆盖,必须主动清)
        let second = stage_sidecar(&src, &home, "0.2.0").unwrap();
        assert!(!first.bin.exists(), "旧版本副本应被清理: {}", first.bin.display());
        assert!(!tmp_of(&second.bin).exists());

        let names: Vec<String> = std::fs::read_dir(&bin_dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().to_string())
            .collect();
        assert_eq!(names, vec![second.bin.file_name().unwrap().to_string_lossy().to_string()]);
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn prune_never_touches_the_data_directory() {
        let base = tmp_dir("prune-data");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");
        let data = home.join("bin").join("gah-data");
        std::fs::create_dir_all(data.join("sessions")).unwrap();
        std::fs::write(data.join("sessions/a.json"), b"{}").unwrap();

        stage_sidecar(&src, &home, "0.1.0").unwrap();
        stage_sidecar(&src, &home, "0.2.0").unwrap();
        assert!(data.join("sessions/a.json").exists(), "清理绝不能碰到 gah-data/");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn stage_missing_source_is_explicit_error() {
        let base = tmp_dir("missing");
        let home = base.join("home");
        let err = stage_sidecar(&base.join("nope"), &home, "0.1.0").unwrap_err();
        assert!(err.contains("读"), "错误应说明读取失败: {err}");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn stage_never_overwrites_an_existing_copy() {
        // 回归(v0.1.3 真机):「替换 …\bin\gah.exe 失败:拒绝访问。(os error 5)」——
        // 固定文件名 + rename 覆盖,一遇到旧副本还在运行(Windows 不允许覆盖运行中的 exe)
        // 就必然失败,整套外置随之降级、数据退回应用目录内。内容寻址后目标名必然不同。
        let base = tmp_dir("nooverwrite");
        let src = fake_sidecar(&base, b"new");
        let home = base.join("home");
        std::fs::create_dir_all(home.join("bin")).unwrap();
        let legacy = home.join("bin").join(exe_name()); // 旧命名的「正在运行的副本」
        std::fs::write(&legacy, b"old-running").unwrap();

        let out = stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(out.staged);
        assert_ne!(out.bin, legacy, "必须换名落地,而不是覆盖旧副本");
        assert_eq!(std::fs::read(&out.bin).unwrap(), b"new");
        assert!(!legacy.exists(), "旧命名副本应被尽力清理");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[cfg(unix)]
    #[test]
    fn staged_bin_is_executable() {
        use std::os::unix::fs::PermissionsExt;
        let base = tmp_dir("exec");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");
        let out = stage_sidecar(&src, &home, "0.1.0").unwrap();
        let mode = std::fs::metadata(&out.bin).unwrap().permissions().mode();
        assert_eq!(mode & 0o111, 0o111, "暂存二进制必须可执行,实际模式 {mode:o}");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn migrate_from_copies_once_and_never_deletes_legacy() {
        let base = tmp_dir("migrate");
        let legacy = base.join("app/gah-data");
        std::fs::create_dir_all(legacy.join("config")).unwrap();
        std::fs::write(legacy.join("config/provider.yaml"), b"key: k").unwrap();
        let home = base.join("home");

        let moved = migrate_from(&legacy, &home).unwrap().expect("应迁移");
        assert_eq!(std::fs::read(moved.join("config/provider.yaml")).unwrap(), b"key: k");
        assert!(legacy.exists(), "旧副本必须保留(只复制不删除)");

        // 已有新数据 → 不再迁移(不覆盖)
        assert!(migrate_from(&legacy, &home).unwrap().is_none());

        // 旧数据不存在 → 无事发生
        assert!(migrate_from(&base.join("gone"), &home).unwrap().is_none());
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn copy_tree_keeps_nested_files() {
        let base = tmp_dir("tree");
        let src = base.join("src");
        std::fs::create_dir_all(src.join("a/b")).unwrap();
        std::fs::write(src.join("a/b/c.txt"), b"deep").unwrap();
        let dst = base.join("dst");
        copy_tree(&src, &dst).unwrap();
        assert_eq!(std::fs::read(dst.join("a/b/c.txt")).unwrap(), b"deep");
        let _ = std::fs::remove_dir_all(&base);
    }
}
