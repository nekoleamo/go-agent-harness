// 内嵌/伴生资产访问:zip part(docx/pptx 的 media)与同目录文件(markdown 图片)。
// ID 为不透明哈希,不暴露宿主路径;预算与 zip 炸弹防护与抽取阶段共用(见 budget.go)。
package hostdocview

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 资产来源类型。
const (
	assetKindZip  = "zip"
	assetKindFile = "file"
)

// assetID 由(文件绝对路径, part 名)派生不透明 ID。
func assetID(abs, part string) string {
	sum := sha256.Sum256([]byte(abs + "\x00" + part))
	return hex.EncodeToString(sum[:])[:24]
}

// openAsset 按 ref 打开资产;返回的 reader 一并负责其容器句柄。
func (s *Service) openAsset(_ context.Context, ref assetRef) (io.ReadCloser, string, error) {
	if ref.kind == assetKindFile {
		f, err := os.Open(ref.abs)
		if err != nil {
			return nil, "", fmt.Errorf("%w: 打开资产失败: %v", sdk.ErrDocNotFound, err)
		}
		return f, ref.mime, nil
	}
	zr, err := zip.OpenReader(ref.abs)
	if err != nil {
		return nil, "", fmt.Errorf("%w: 打开容器失败: %v", sdk.ErrDocParse, err)
	}
	zb := newZipBudget(s.budget)
	for _, f := range zr.File {
		if f.Name != ref.part {
			continue
		}
		if err := zb.check(f.Name, int64(f.UncompressedSize64), int64(f.CompressedSize64)); err != nil {
			_ = zr.Close()
			return nil, "", err
		}
		rc, err := f.Open()
		if err != nil {
			_ = zr.Close()
			return nil, "", fmt.Errorf("%w: 读取资产 %s 失败: %v", sdk.ErrDocParse, ref.part, err)
		}
		return &assetReader{rc: rc, zr: zr}, ref.mime, nil
	}
	_ = zr.Close()
	return nil, "", fmt.Errorf("%w: 容器中不存在资产 %s", sdk.ErrDocUnsupported, ref.part)
}

// assetReader 让 zip part 与其所属 zip 句柄一起关闭。
type assetReader struct {
	rc io.ReadCloser
	zr *zip.ReadCloser
}

func (a *assetReader) Read(p []byte) (int, error) { return a.rc.Read(p) }

func (a *assetReader) Close() error {
	return errors.Join(a.rc.Close(), a.zr.Close())
}
