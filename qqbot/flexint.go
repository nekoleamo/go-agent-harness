// flexInt 兼容 JSON 数字与"字符串数字"。
// QQ 官方部分字段实际返回字符串(文档示例 expires_in: "7200" 即为字符串),
// 直接按 int 解析会解码失败导致整条响应不可用(token 拿不到 → 网关连不上)。
package qqbot

import (
	"fmt"
	"strconv"
	"strings"
)

// flexInt JSON 数字/字符串数字/空值/null 一律可解(其余报错)。
type flexInt int

// UnmarshalJSON 实现 json.Unmarshaler(宽容:去引号后按整数→浮点解析)。
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		*f = flexInt(n)
		return nil
	}
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(fl)
		return nil
	}
	return fmt.Errorf("flexInt: 非法数值 %q", s)
}
