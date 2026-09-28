// Package instructions 全局指令文件($GAH_HOME/AGENTS.md)的读写单一出口。
//
// 为什么单独一个包:这份文件是「用户级指令」,对**所有**角色生效(角色未声明
// exclude_global 时),因此有三个读方(host-system-prompt 注入、Web 面板展示、
// 审批面判定)和一个写方(Web 面板)。路径与上限在这里只写一遍,避免"三处各拼一次
// filepath.Join(sdk.Home(), …)"漂移(同 internal/skills 对技能文件的处理)。
//
// 边界:只管这一份全局文件;**项目级** <workspace>/AGENTS.md 与 .gah/skills/ 不在此
// 范围(它们在项目仓库里、随 git 走,不进审批面 —— 见 DESIGN §14.1 第八十批第 4 条)。
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// FileName 全局指令文件名(数据根下)。
	FileName = "AGENTS.md"
	// MaxBytes 全局指令的字节上限(与 roles.MaxAgentsBytes 同值:两份指令都逐字进
	// 系统提示,上限不一致会让"这里能存那里不能"变成悬案)。写入超限显式失败,
	// 不静默截断 —— 截断一份指令文件等于悄悄改用户的话。
	MaxBytes = 32 * 1024
)

// ErrTooLarge 正文超上限(哨兵:调用方按它区分"用户要改小"与"盘写不进去" ——
// 前者是 400,后者是 500,报错的状态码本身就在告诉面板"该提醒用户改内容"还是"该查环境")。
var ErrTooLarge = errors.New("全局指令超上限")

// Path 全局指令文件绝对路径($GAH_HOME/AGENTS.md;空数据根 → TempDir 兜底,由 sdk.Home 决定)。
func Path() string { return filepath.Join(sdk.Home(), FileName) }

// Exists 文件是否存在。
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Read 读全文(文件不存在 → ("", false, nil):"没有全局指令"是合法状态,不是错误)。
func Read() (string, bool, error) {
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

// Write 原子写全局指令(超上限显式失败;目录不存在则创建)。
//
// 调用方(web 面板)写完必须让宿主重读指令文件(ReloadInstructions),否则文件变了、
// 本轮系统提示还是旧的 —— 那正是"保存了但不生效"的悬案。
func Write(text string) error {
	if len(text) > MaxBytes {
		return fmt.Errorf("%w:%d 字节 > %d 字节(会逐字进系统提示,请精简)", ErrTooLarge, len(text), MaxBytes)
	}
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("数据根不可写 %s: %w", dir, err)
	}
	if err := writeFileAtomic(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("全局指令写入失败: %w", err)
	}
	return nil
}

// Shrink 按上限截断(注入侧用;返回是否发生截断)。
func Shrink(text string) (string, bool) {
	if len(text) <= MaxBytes {
		return text, false
	}
	// 按 rune 边界截断:切在半个 UTF-8 字符中间会产出非法文本
	cut := MaxBytes
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// utf8Start 该字节是否为 UTF-8 起始字节(续字节形如 10xxxxxx)。
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// writeFileAtomic 同目录临时文件 + rename 原子替换(与 internal/prefs、internal/roles、
// internal/skills 同款约定:读方永不看到半截文件;失败清理临时文件)。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gah-instructions-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
