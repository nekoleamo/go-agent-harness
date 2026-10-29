// pack:把一个文件打成 zip(window 侧更新产物用)。
//
// 为什么要自己实现:wails updater 要求 zip 里**恰好一个顶层条目**,而各平台的打包工具
// 对路径/时间戳的处理不同(时间戳还影响可复现性)。用标准库自己写,条目名与时间戳都固定,
// 同一输入两次打包逐字节一致。
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	in := fs.String("in", "", "要打进 zip 的文件或目录")
	entry := fs.String("entry", "", "zip 内的顶层条目名(缺省用文件名)")
	out := fs.String("out", "", "输出 zip 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("缺 --in 或 --out")
	}
	name := *entry
	if name == "" {
		name = filepath.Base(*in)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	// 固定时间戳:同一输入两次打包逐字节一致(便于 CI 比对/缓存)。
	fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	st, err := os.Stat(*in)
	if err != nil {
		return err
	}
	if st.IsDir() {
		err = filepath.Walk(*in, func(p string, info os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			rel, rerr := filepath.Rel(*in, p)
			if rerr != nil {
				return rerr
			}
			return addZipEntry(zw, p, filepath.ToSlash(filepath.Join(name, rel)), info, fixed)
		})
	} else {
		err = addZipEntry(zw, *in, name, st, fixed)
	}
	if err != nil {
		_ = zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已打包 %s → %s(顶层条目 %s)\n", *in, *out, name)
	return nil
}

func addZipEntry(zw *zip.Writer, src, name string, info os.FileInfo, fixed time.Time) error {
	if info.IsDir() {
		_, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/", Modified: fixed, Method: zip.Store})
		return err
	}
	hdr := &zip.FileHeader{Name: name, Modified: fixed, Method: zip.Deflate}
	hdr.SetMode(info.Mode())
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	_, err = io.Copy(w, r)
	return err
}
