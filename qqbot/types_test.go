package qqbot

import (
	"encoding/json"
	"testing"
)

// TestGroupAtMessageOfficialShape 真机事件体解析:mentions 可缺省/为空(不含机器人自身),
// author 携带 bot/member_role;引用消息(message_type=103)正文在 msg_elements。
func TestGroupAtMessageOfficialShape(t *testing.T) {
	raw := []byte(`{"id":"m1","author":{"id":"A1","member_openid":"A1","member_role":"member","username":"小明","bot":false},
		"content":" /今日天气 ","group_openid":"G1","message_type":0,"timestamp":"2026-07-21T10:00:00+08:00"}`)
	var m GroupAtMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Author.MemberOpenID != "A1" || m.Author.Username != "小明" || m.Author.Bot || m.Author.MemberRole != "member" {
		t.Fatalf("author 解析不符: %+v", m.Author)
	}
	if len(m.Mentions) != 0 {
		t.Fatalf("mentions 缺省应为空: %+v", m.Mentions)
	}
	// 引用消息:content 空,正文在 msg_elements(含嵌套)
	ref := []byte(`{"id":"m2","group_openid":"G1","content":"","message_type":103,
		"msg_elements":[{"message_type":103,"content":"引用正文","msg_elements":[{"content":"嵌套正文"}]}]}`)
	var r GroupAtMessage
	if err := json.Unmarshal(ref, &r); err != nil {
		t.Fatal(err)
	}
	if got := ElementsText(r.MsgElements); got != "引用正文\n嵌套正文" {
		t.Fatalf("msg_elements 文本提取: %q", got)
	}
	if ElementsText(nil) != "" {
		t.Fatal("空元素应返回空串")
	}
}

// TestMentionIsUserSchema mentions 元素即官方 User schema(bot 标志可用于区分机器人)。
func TestMentionIsUserSchema(t *testing.T) {
	var ms []Mention
	if err := json.Unmarshal([]byte(`[{"id":"BOT","username":"机器人","bot":true,"union_openid":"U"}]`), &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || !ms[0].Bot || ms[0].Username != "机器人" || ms[0].UnionOpenID != "U" {
		t.Fatalf("mention 解析不符: %+v", ms)
	}
}
