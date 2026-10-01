package web

// 技能包端点的 HTTP 语义(第一百零六批)。包格式/安全校验在 internal/skillpack 的
// 12 条单测里;这里只钉「Web 这一层该有的行为」:下载头、multipart、状态码、
// 未装配服务时的 503、以及导入后**让索引跟上**(否则技能落盘了却看不见)。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/skillpack"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// skillStub 实现 sdk.SkillsService(记 Rescan 次数,验证导入后真的重扫了)。
type skillStub struct {
	rescans int
}

func (s *skillStub) List() []sdk.SkillInfo { return nil }

func (s *skillStub) SetFilter(func(sdk.SkillInfo) bool) sdk.Disposer {
	return func() {}
}

func (s *skillStub) Rescan() error { s.rescans++; return nil }

func newSkillPackServer(t *testing.T) (*Server, *skillStub) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	s, _ := newTestServer()
	stub := &skillStub{}
	s.roleSkills = stub
	return s, stub
}

func seedSkill(t *testing.T, name, desc string) {
	t.Helper()
	body := skills.Content(name, desc, []string{"触发 " + name}, "正文内容。\n")
	if err := skills.Shared().Write(name, body, false); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPackGetDownloadsZip(t *testing.T) {
	s, _ := newSkillPackServer(t)
	seedSkill(t, "notes", "整理笔记")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/skillpack/notes", nil)
	req.SetPathValue("name", "notes") // 直接调 handler 时 mux 不会填,得自己设
	s.handleSkillPackGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 应为 application/zip,got %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "gah-skill-notes.zip") {
		t.Fatalf("下载文件名不对: %q", cd)
	}
	if !strings.HasPrefix(rec.Body.String(), "PK") {
		t.Fatal("响应体应是 zip")
	}
}

func TestSkillPackGetMissingSkill(t *testing.T) {
	s, _ := newSkillPackServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/skillpack/nope", nil)
	req.SetPathValue("name", "nope")
	s.handleSkillPackGet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在的技能应 400,got %d", rec.Code)
	}
}

// postPack 组装一次 multipart 上传请求。
func postPack(t *testing.T, pack []byte, query string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "gah-skill-x.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(pack); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/skillpack"+query, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestSkillPackPostImportsAndRescans(t *testing.T) {
	s, stub := newSkillPackServer(t)
	seedSkill(t, "src", "原始技能")
	pack, err := skillpack.Export("src")
	if err != nil {
		t.Fatal(err)
	}
	// 换个数据根 = 模拟"别人的 gah"
	t.Setenv("GAH_HOME", t.TempDir())

	rec := httptest.NewRecorder()
	s.handleSkillPackPost(rec, postPack(t, pack, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200,got %d: %s", rec.Code, rec.Body.String())
	}
	if !skills.Shared().Exists("src") {
		t.Fatal("导入后技能不可读")
	}
	if stub.rescans == 0 {
		t.Fatal("导入后必须 Rescan(否则新技能在索引里看不见)")
	}
}

func TestSkillPackPostAsRenames(t *testing.T) {
	s, _ := newSkillPackServer(t)
	seedSkill(t, "origin", "原始")
	pack, err := skillpack.Export("origin")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	rec := httptest.NewRecorder()
	s.handleSkillPackPost(rec, postPack(t, pack, "?as=copy1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("改名导入应 200,got %d: %s", rec.Code, rec.Body.String())
	}
	if !skills.Shared().Exists("copy1") {
		t.Fatal("改名导入后目标名不存在")
	}
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Name string `json:"name"`
			From string `json:"from"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("回执不是 JSON: %v", err)
	}
	if !out.OK || out.Result.Name != "copy1" || out.Result.From != "origin" {
		t.Fatalf("回执应说清新名与原名: %+v", out)
	}
}

func TestSkillPackPostRejectsDuplicate(t *testing.T) {
	s, _ := newSkillPackServer(t)
	seedSkill(t, "dup", "原始")
	pack, err := skillpack.Export("dup")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleSkillPackPost(rec, postPack(t, pack, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("同名缺省应拒(400),got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "已存在") {
		t.Fatalf("错误文案应说明是同名冲突: %s", rec.Body.String())
	}
	// 显式覆盖:允许
	rec2 := httptest.NewRecorder()
	s.handleSkillPackPost(rec2, postPack(t, pack, "?overwrite=1"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("显式覆盖应 200,got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestSkillPackPostRejectsNonMultipart(t *testing.T) {
	s, _ := newSkillPackServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/skillpack", strings.NewReader("not multipart"))
	s.handleSkillPackPost(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 multipart 应 400,got %d", rec.Code)
	}
}

func TestSkillPackPostMissingFileField(t *testing.T) {
	s, _ := newSkillPackServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("wrong", "x")
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/skillpack", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleSkillPackPost(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "file") {
		t.Fatalf("缺 file 字段应 400 且说清缺什么,got %d: %s", rec.Code, rec.Body.String())
	}
}
