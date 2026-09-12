// stage.rs 运行态外置(NOND-W2b-α):让用户数据不在应用目录里。
//
// 问题:便携纪律(不变)规定数据根 = **二进制同级** `gah-data/`,单一解析链、不读 env/不传参。
// 但桌面壳里 sidecar 位于 `.app/Contents/MacOS/gah`(mac)或 NSIS 安装目录(win),于是
// 「二进制同级」= 应用目录内 → 升级整包替换会带走数据、卸载(NSIS 删安装目录)会删数据。
//
// 解法:壳先把 sidecar **复制到用户数据目录**,再从那里 spawn。二进制落在用户目录 →
// 同级 `gah-data/` 自然在应用目录之外,而数据根解析规则**一个字没改**:
//
//   mac   ~/Library/Application Support/dev.gah.desktop/bin/{gah, gah-data/}
//   win   %LOCALAPPDATA%\dev.gah.desktop\bin\{gah.exe, gah-data\}
//
// 注意区别:这**不是**「壳告诉 gah 去哪写数据」(那是 2026-09-16 明确移除的 appDataHome 方案,
// 会破唯一解析链),而是「把二进制搬到用户可写目录,让既有规则自己得出结论」。
//
// 复制时机:标记文件记录的「版本 + 源文件大小 + 源 mtime」与实际不符时重放(升级、重新构建)。
// 替换是原子的(临时文件 → rename),失败返回 Err(调用方告警后回退到应用目录内运行,
// 绝不静默降级)。

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

/// staged_bin 外置后的二进制路径(`<home>/bin/gah[.exe]`)。
pub fn staged_bin(home: &Path) -> PathBuf {
    home.join("bin").join(exe_name())
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

/// fingerprint 暂存指纹:版本 + 源文件大小 + 源 mtime(nanos)。任一变化即重放
/// (开发期同版本重建也能生效)。
fn fingerprint(src: &Path, version: &str) -> Result<String, String> {
    let md = std::fs::metadata(src).map_err(|e| format!("读 {} 失败: {e}", src.display()))?;
    let mtime = md
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    Ok(format!("{version}\n{}\n{mtime}\n", md.len()))
}

/// StageOutcome stage_sidecar 的结果。
#[derive(Debug)]
pub struct StageOutcome {
    pub bin: PathBuf,
    /// staged=true 表示本次真的复制了(升级/首次/重建);false = 已是最新,跳过。
    pub staged: bool,
}

/// stage_sidecar 把 sidecar 复制到 `<home>/bin/`(必要时),返回可执行路径。
pub fn stage_sidecar(src: &Path, home: &Path, version: &str) -> Result<StageOutcome, String> {
    let bin_dir = home.join("bin");
    std::fs::create_dir_all(&bin_dir).map_err(|e| format!("建 {} 失败: {e}", bin_dir.display()))?;
    let dst = staged_bin(home);
    let marker = bin_dir.join(".gah-stage");
    let want = fingerprint(src, version)?;
    if dst.exists() {
        if let Ok(have) = std::fs::read_to_string(&marker) {
            if have == want {
                return Ok(StageOutcome { bin: dst, staged: false });
            }
        }
    }
    // 原子替换:先写 `.new`,再 rename 覆盖(同目录 rename 在 mac/win 都是原子替换)。
    let tmp = bin_dir.join(format!("{}.new", exe_name()));
    if tmp.exists() {
        let _ = std::fs::remove_file(&tmp);
    }
    std::fs::copy(src, &tmp)
        .map_err(|e| format!("复制 {} → {} 失败: {e}", src.display(), tmp.display()))?;
    make_executable(&tmp)?;
    strip_mark_of_transfer(&tmp);
    std::fs::rename(&tmp, &dst).map_err(|e| format!("替换 {} 失败: {e}", dst.display()))?;
    let mtmp = bin_dir.join(".gah-stage.new");
    std::fs::write(&mtmp, want.as_bytes()).map_err(|e| format!("写标记失败: {e}"))?;
    std::fs::rename(&mtmp, &marker).map_err(|e| format!("更新标记失败: {e}"))?;
    Ok(StageOutcome { bin: dst, staged: true })
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
        assert_eq!(data_root(&home), staged_bin(&home).parent().unwrap().join("gah-data"));
        assert_eq!(staged_bin(&home), home.join("bin").join(exe_name()));
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
    fn stage_is_atomic_and_leaves_no_tmp() {
        let base = tmp_dir("atomic");
        let src = fake_sidecar(&base, b"v1");
        let home = base.join("home");
        stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(!home.join("bin").join(format!("{}.new", exe_name())).exists());
        let entries: Vec<String> = std::fs::read_dir(home.join("bin"))
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().to_string())
            .collect();
        assert!(entries.contains(&exe_name().to_string()));
        assert!(entries.contains(&".gah-stage".to_string()));
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
    fn stage_overwrites_existing_bin() {
        let base = tmp_dir("overwrite");
        let src = fake_sidecar(&base, b"new");
        let home = base.join("home");
        std::fs::create_dir_all(home.join("bin")).unwrap();
        std::fs::write(staged_bin(&home), b"old").unwrap();
        let out = stage_sidecar(&src, &home, "0.1.0").unwrap();
        assert!(out.staged);
        assert_eq!(std::fs::read(&out.bin).unwrap(), b"new");
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
