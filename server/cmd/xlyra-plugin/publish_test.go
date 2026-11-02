package main

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xlyra/server/internal/jsplugin"
)

func TestParsePublishArgs(t *testing.T) {
	opts, err := parsePublishArgs([]string{"--changelog", "修复", "dist/a.xlp"}, "from-env")
	if err != nil {
		t.Fatal(err)
	}
	if opts.token != "from-env" || opts.cloud != defaultCloudURL || opts.changelog != "修复" {
		t.Fatalf("%+v", opts)
	}
	opts, err = parsePublishArgs([]string{"--token", "from-flag", "--changelog", "修复", "--cloud", "https://cloud.example", "dist/a.xlp"}, "from-env")
	if err != nil || opts.token != "from-flag" || opts.cloud != "https://cloud.example" {
		t.Fatalf("%v %+v", err, opts)
	}
	if _, err := parsePublishArgs([]string{"--changelog", "修复", "--cloud", "http://example.com", "a.xlp"}, "t"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("非 https: %v", err)
	}
	if _, err := parsePublishArgs([]string{"--changelog", "修复", "--cloud", "http://10.1.2.3:8080", "a.xlp"}, "t"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("内网 http: %v", err)
	}
	opts, err = parsePublishArgs([]string{"--changelog", "修复", "--cloud", "http://127.0.0.1:8090", "a.xlp"}, "t")
	if err != nil || opts.cloud != "http://127.0.0.1:8090" {
		t.Fatalf("本机 http: %v %+v", err, opts)
	}
	opts, err = parsePublishArgs([]string{"--changelog", "修复", "--cloud", "http://localhost:8090/", "a.xlp"}, "t")
	if err != nil || opts.cloud != "http://localhost:8090" {
		t.Fatalf("localhost: %v %+v", err, opts)
	}
	if _, err := parsePublishArgs([]string{"--changelog", "修复", "a.xlp"}, ""); err == nil || !strings.Contains(err.Error(), "XLYRA_CLOUD_TOKEN") {
		t.Fatal(err)
	}
	if _, err := parsePublishArgs([]string{"--token", "t", "a.xlp"}, ""); err == nil || !strings.Contains(err.Error(), "更新日志") {
		t.Fatal(err)
	}
	usageText := captureStderr(t, usage)
	if !strings.Contains(usageText, "publish") || !strings.Contains(usageText, "XLYRA_CLOUD_TOKEN") || !strings.Contains(usageText, "shell 历史") {
		t.Fatalf("帮助缺少 publish:\n%s", usageText)
	}
}

func TestPublishUploadsAndPolls(t *testing.T) {
	raw := signedPlugin(t)
	var polls atomic.Int32
	var gotAuth, gotLog string
	var gotPkg []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "\n") {
			t.Fatal("令牌头异常")
		}
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/dev/token/submissions":
			media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.HasPrefix(media, "multipart/") {
				t.Fatalf("content-type %s", r.Header.Get("Content-Type"))
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(part)
				if part.FormName() == "package" {
					gotPkg = data
				}
				if part.FormName() == "changelog" {
					gotLog = string(data)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"sub-1","status":"checking"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/dev/token/submissions/sub-1":
			n := polls.Add(1)
			status := "checking"
			if n >= 2 {
				status = "in_review"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"sub-1","status":"`+status+`","checkSummary":"","reviewNote":""}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "plugin.xlp")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	token := "xlh_" + strings.Repeat("cd", 32)
	var out bytes.Buffer
	err := publishPackage(publishOptions{
		packagePath: path, changelog: "修复额度解析", cloud: srv.URL, token: token,
		timeout: time.Minute, pollInterval: 5 * time.Millisecond, client: srv.Client(), out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("认证头 %q", gotAuth)
	}
	if gotLog != "修复额度解析" || !bytes.Equal(gotPkg, raw) {
		t.Fatalf("上传内容 log=%q pkg=%d", gotLog, len(gotPkg))
	}
	text := out.String()
	if !strings.Contains(text, "提交编号 sub-1") || !strings.Contains(text, "待审核") || strings.Contains(text, token) {
		t.Fatalf("输出:\n%s", text)
	}
	if polls.Load() < 2 {
		t.Fatalf("轮询次数 %d", polls.Load())
	}
}

func TestPublishNoWaitAndErrors(t *testing.T) {
	raw := signedPlugin(t)
	path := filepath.Join(t.TempDir(), "plugin.xlp")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	token := "xlh_" + strings.Repeat("ef", 32)
	var gets atomic.Int32
	var uploaded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
		}
		if r.Method == http.MethodPost && !strings.Contains(r.URL.Path, "fail") {
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(part)
				if part.FormName() == "changelog" {
					uploaded = string(data)
				}
			}
		}
		if r.URL.Query().Get("mode") == "fail" || strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"not_developer","message":"`+token+`"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sub-2","status":"checking"}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := publishPackage(publishOptions{
		packagePath: path, changelog: "说明", cloud: srv.URL, token: token, noWait: true,
		client: srv.Client(), out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 0 || !strings.Contains(out.String(), "检查中") || strings.Contains(out.String(), token) {
		t.Fatalf("no-wait gets=%d\n%s", gets.Load(), out.String())
	}

	out.Reset()
	err := publishPackage(publishOptions{
		packagePath: path, changelog: "说明", cloud: srv.URL + "/fail", token: token,
		client: srv.Client(), out: &out,
	})
	if err == nil || !strings.Contains(err.Error(), "不是有效开发者") || strings.Contains(err.Error(), token) || strings.Contains(out.String(), token) {
		t.Fatalf("err=%v\n%s", err, out.String())
	}

	note, err := os.CreateTemp(t.TempDir(), "log-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := note.WriteString("  从文件来的说明 \n"); err != nil {
		t.Fatal(err)
	}
	note.Close()
	opts, err := parsePublishArgs([]string{"--changelog-file", note.Name(), "--cloud", srv.URL, path}, token)
	if err != nil {
		t.Fatal(err)
	}
	opts.noWait = true
	opts.client = srv.Client()
	opts.out = &out
	out.Reset()
	if err := publishPackage(opts); err != nil {
		t.Fatal(err)
	}
	if uploaded != "从文件来的说明" {
		t.Fatalf("文件更新日志 %q", uploaded)
	}
}

func TestPublishRejectsUnsignedAndTimeout(t *testing.T) {
	dir := newProject(t, jsplugin.KindQuotaProbe, "acme-unsigned")
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unsigned.xlp")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	err = publishPackage(publishOptions{packagePath: path, changelog: "说明", cloud: "https://xlyra.yachiyo.im", token: "secret-token"})
	if err == nil || !strings.Contains(err.Error(), "未签名") || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}

	signed := signedPlugin(t)
	signedPath := filepath.Join(t.TempDir(), "signed.xlp")
	if err := os.WriteFile(signedPath, signed, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"sub-3","status":"checking"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"sub-3","status":"checking"}`))
	}))
	defer srv.Close()
	err = publishPackage(publishOptions{
		packagePath: signedPath, changelog: "说明", cloud: srv.URL, token: "xlh_timeouttoken",
		timeout: 40 * time.Millisecond, pollInterval: 5 * time.Millisecond, client: srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "等待超时") || strings.Contains(err.Error(), "xlh_timeouttoken") {
		t.Fatal(err)
	}
}

func signedPlugin(t *testing.T) []byte {
	t.Helper()
	dir := newProject(t, jsplugin.KindQuotaProbe, "acme-publish")
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jsplugin.AttachSignature(raw, priv)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
