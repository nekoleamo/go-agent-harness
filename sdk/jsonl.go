package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// tailScanLimit 残行检查的尾部窗口上限(单条记录上限;超出说明文件异常,不猜不截)。
const tailScanLimit = 1 << 20

// AppendJSONLine 以 jsonl 语义追加一条记录(单行 JSON + 换行;0600,进程内不共享 fd)。
//
// 为什么不能直接 O_APPEND:上次写入若被断电/磁盘满截断,文件尾会留下半行 JSON,
// 直接追加会让**残行与新记录粘成一条坏行**——读侧按行解析失败即跳过,于是两条记录
// 一起丢失(静默数据丢失)。此处先修尾:残行不可解析 → 截断到上一完整行;残行本身
// 合法只是缺换行 → 补换行。
func AppendJSONLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("jsonl: 编码: %w", err)
	}
	if err := repairJSONTail(path); err != nil {
		return fmt.Errorf("jsonl: 修尾: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("jsonl: 追加: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("jsonl: 写入: %w", err)
	}
	return nil
}

// repairJSONTail 修复尾部残行(见 AppendJSONLine);文件不存在/尾部完整即无操作。
func repairJSONTail(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size == 0 {
		return nil
	}
	start := int64(0)
	if size > tailScanLimit {
		start = size - tailScanLimit
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(tail) == 0 || tail[len(tail)-1] == '\n' {
		return nil // 尾部完整
	}
	i := bytes.LastIndexByte(tail, '\n')
	var partial []byte
	switch {
	case i >= 0:
		partial = tail[i+1:]
	case start > 0:
		return nil // 尾部窗口内无换行 = 超长单行(>1MiB):不猜不截,交读侧跳过
	default:
		partial = tail // 整个文件就这一行(无换行)
	}
	if len(partial) == 0 {
		return nil
	}
	if json.Valid(partial) {
		// 残行本身是合法 JSON,只缺换行:补上,避免与新记录粘成坏行
		af, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer af.Close()
		_, err = af.Write([]byte{'\n'})
		return err
	}
	if i < 0 {
		return nil // 唯一一行且不可解析:无可用前缀,不截断(保守,不毁数据)
	}
	return os.Truncate(path, start+int64(i)+1)
}
