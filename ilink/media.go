// 媒体入站(P0-2c):iLink CDN 媒体下载与解密 helper。图片/文件媒体项
// (ImageItem/FileItem)经 EncryptQueryParam URL 下载,payload 以 aeskey 做
// AES-128-ECB 解密(PKCS7)。解析失败宽容降级(返回原文,不因解密异常丢内容)。
package ilink

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxMediaBytes 单媒体下载上限(入站媒体超限拒绝,防内存放大)。
const maxMediaBytes = 20 << 20 // 20MB

// mediaHTTP 下载用 http client(对齐消息接口的短超时;媒体通常远小于窗口)。
var mediaHTTP = &http.Client{Timeout: 30 * time.Second}

// DownloadMedia 下载 CDN 媒体字节(带大小上限与超时)。
func DownloadMedia(ctx context.Context, url string) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("ilink: 媒体 URL 为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ilink: 媒体下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ilink: 媒体下载 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMediaBytes {
		return nil, fmt.Errorf("ilink: 媒体超 20MB 上限")
	}
	return data, nil
}

// DecryptMedia 以 aeskey 解密媒体字节(AES-128-ECB + PKCS7)。
// key 宽容解析:hex → base64 → 原样(须 16 字节);解密失败(长度不对齐/坏 key)返回
// 原文 + 错误(调用方决定降级)——实测不同 CDN 对同字段编码不一,不做死假设。
func DecryptMedia(data []byte, aesKey string) ([]byte, error) {
	if len(data) == 0 || aesKey == "" {
		return data, nil
	}
	if len(data)%aes.BlockSize != 0 {
		return data, fmt.Errorf("ilink: 密文长度 %d 非 16 对齐(可能未加密)", len(data))
	}
	key, err := parseAESKey(aesKey)
	if err != nil {
		return data, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return data, err
	}
	out := make([]byte, len(data))
	// ECB(标准库不提供):逐 16 字节块独立解密
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	// PKCS7 去填充
	n := int(out[len(out)-1])
	if n <= 0 || n > aes.BlockSize || n > len(out) {
		return out, nil // 填充异常:宽容返回解密结果(尾部多字节,调用方按媒体解析)
	}
	return out[:len(out)-n], nil
}

// parseAESKey 宽容解析 16 字节媒体密钥:hex → base64 → 原样。
func parseAESKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == aes.BlockSize {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == aes.BlockSize {
		return b, nil
	}
	if b := []byte(s); len(b) == aes.BlockSize {
		return b, nil
	}
	return nil, fmt.Errorf("ilink: aeskey 无法解析为 16 字节密钥")
}

// ExtFromURL 从媒体 URL 猜测扩展名(.jpg/.png/.gif/.mp4/…;无扩展返回空)。
func ExtFromURL(u string) string {
	i := strings.LastIndex(u, "?")
	if i >= 0 {
		u = u[:i]
	}
	slash := strings.LastIndex(u, "/")
	if slash >= 0 {
		u = u[slash+1:]
	}
	dot := strings.LastIndex(u, ".")
	if dot < 0 || dot == len(u)-1 {
		return ""
	}
	ext := strings.ToLower(u[dot+1:])
	if len(ext) > 8 || strings.ContainsAny(ext, "/\\%") {
		return ""
	}
	return ext
}
