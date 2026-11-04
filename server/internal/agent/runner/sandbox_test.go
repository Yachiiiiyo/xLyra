package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSandboxConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sandbox  *SandboxConfig
		isolated bool
		wantErr  bool
	}{
		{name: "nil"},
		{name: "仅 Pi", sandbox: &SandboxConfig{PiLandlock: true}},
		{name: "bash 无 PiLandlock", sandbox: &SandboxConfig{Bash: true}, isolated: true, wantErr: true},
		{name: "bash 无隔离", sandbox: &SandboxConfig{PiLandlock: true, Bash: true}, wantErr: true},
		{name: "完整", sandbox: &SandboxConfig{PiLandlock: true, Bash: true}, isolated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, "basic")
			cfg := env.runner.cfg
			cfg.Sandbox = tc.sandbox
			cfg.Isolation = nil
			if tc.isolated {
				cfg.Isolation = &IsolationConfig{}
			}
			r, err := New(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && tc.sandbox != nil && (r.cfg.Sandbox.ExecDefaultTimeout != 120*time.Second || r.cfg.Sandbox.ExecMaxTimeout != 600*time.Second) {
				t.Fatalf("默认超时未补全: %+v", r.cfg.Sandbox)
			}
		})
	}
}

func TestGatewayConnectPort(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "http://127.0.0.1:8080/base", want: 8080},
		{raw: "http://localhost", want: 80},
		{raw: "https://[::1]", want: 443},
		{raw: "ftp://localhost", wantErr: true},
		{raw: "http://localhost:bad", wantErr: true},
		{raw: "://", wantErr: true},
	} {
		got, err := gatewayConnectPort(tc.raw)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("gatewayConnectPort(%q) = %d, %v; want %d, error=%v", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestPIReadOnlyPaths(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	makeFile := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	localNode := filepath.Join(root, "usr/local/bin/node")
	optNode := filepath.Join(root, "opt/node-v22/bin/node")
	entry := filepath.Join(root, "app/node_modules/pi/dist/cli.js")
	plainEntry := filepath.Join(root, "plain/cli.js")
	extension := filepath.Join(root, "ext/xlyra.ts")
	skill := filepath.Join(root, "skills/demo")
	pathDir := filepath.Join(root, "path")
	for _, path := range []string{localNode, optNode, entry, plainEntry, extension, pathDir + "/tool"} {
		makeFile(path)
	}
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-node")
	if err := os.Symlink(optNode, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		command string
		entry   string
		want    []string
	}{
		{name: "local node", command: localNode, entry: plainEntry, want: []string{filepath.Join(root, "usr/local"), filepath.Join(root, "plain"), filepath.Join(root, "ext"), skill, filepath.Dir(skill), pathDir}},
		{name: "opt node 与 node_modules", command: optNode, entry: entry, want: []string{filepath.Join(root, "opt/node-v22"), filepath.Join(root, "app"), filepath.Join(root, "ext"), skill, filepath.Dir(skill), pathDir}},
		{name: "node 符号链接", command: link, entry: entry, want: []string{filepath.Join(root, "opt/node-v22"), filepath.Join(root, "app"), filepath.Join(root, "ext"), skill, filepath.Dir(skill), pathDir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := piReadOnlyPaths(tc.command, tc.entry, extension, []string{skill}, pathDir+string(os.PathListSeparator)+pathDir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunTools(t *testing.T) {
	r := &Runner{tools: "read,write"}
	if got := r.runTools(false, false); got != "read,write" {
		t.Fatal(got)
	}
	if got := r.runTools(true, false); got != "read,write,bash" {
		t.Fatal(got)
	}
	if got := r.runTools(false, true); got != "read,write,image_generation" {
		t.Fatal(got)
	}
	if got := r.runTools(true, true); got != "read,write,bash,image_generation" {
		t.Fatal(got)
	}
}

func TestXlyraConfigBash(t *testing.T) {
	plain := xlyraConfigJSON("full", nil, nil, nil)
	if string(plain) != `{"permissionMode":"full","readRoots":[]}` {
		t.Fatalf("无 bash JSON 改变: %s", plain)
	}
	withBash := string(xlyraConfigJSON("ask", nil, &xlyraBashConfig{Enabled: true, Socket: "/tmp/exec.sock", DefaultTimeout: 120, MaxTimeout: 600}, nil))
	if !strings.Contains(withBash, `"bash":{"enabled":true,"socket":"/tmp/exec.sock","defaultTimeout":120,"maxTimeout":600}`) {
		t.Fatalf("bash 配置缺失: %s", withBash)
	}
}

func TestSandboxSystemPrompt(t *testing.T) {
	r := &Runner{}
	if got := r.systemPrompt(false, 120*time.Second, false); got != defaultSystemPrompt {
		t.Fatal("未开启 bash 时系统提示发生变化")
	}
	got := r.systemPrompt(true, 120*time.Second, false)
	for _, text := range []string{"没有网络", "默认 120 秒", "后台进程都会被清理", "安装依赖"} {
		if !strings.Contains(got, text) {
			t.Errorf("系统提示缺少 %q", text)
		}
	}
	image := r.systemPrompt(false, 120*time.Second, true)
	for _, text := range []string{"image_generation", "images/", "图片内容不会返回给你", "Markdown", "调用次数有限"} {
		if !strings.Contains(image, text) {
			t.Errorf("生图系统提示缺少 %q", text)
		}
	}
	// 提示里不能出现可照抄的图片 Markdown 示例:弱一点的模型没调用工具也会照着写进回复(线上出现过
	// 没生图却在结尾写 ![说明](images/xxx.png)),提示要明确「没调用工具就别写图片引用」。
	if strings.Contains(image, "![") {
		t.Error("生图系统提示不应包含可照抄的图片 Markdown 示例")
	}
	if !strings.Contains(image, "不要在回复里写任何图片引用") {
		t.Error("生图系统提示应明确没调用工具时不要写图片引用")
	}
	if strings.Contains(got, "image_generation") {
		t.Fatal("未启用生图时系统提示不应出现工具说明")
	}
}

func TestXlyraImageGenerationConfig(t *testing.T) {
	r := &Runner{cfg: Config{GatewayBaseURL: "http://127.0.0.1:18080/", ImageModels: func() int { return 1 }}}
	config := r.imageGenerationConfig("send", "/tmp/run/token")
	if config == nil {
		t.Fatal("有生图模型时配置不应为空")
	}
	if config.Endpoint != "http://127.0.0.1:18080/internal/agent-llm/images" || config.TokenFile != "/tmp/run/token" || config.MaxBytes != 33554432 || config.TimeoutSeconds != 660 {
		t.Fatalf("生图配置错误: %+v", config)
	}
	data := string(xlyraConfigJSON("ask", nil, nil, config))
	for _, text := range []string{`"imageGeneration":{"enabled":true`, `"tokenFile":"/tmp/run/token"`, `"maxBytes":33554432`, `"timeoutSeconds":660`} {
		if !strings.Contains(data, text) {
			t.Errorf("xlyra.json 缺少 %s: %s", text, data)
		}
	}
	if got := r.imageGenerationConfig("compact", "/tmp/run/token"); got != nil {
		t.Fatalf("compact 不应启用生图: %+v", got)
	}
	r.cfg.ImageModels = func() int { return 0 }
	if got := r.imageGenerationConfig("send", "/tmp/run/token"); got != nil {
		t.Fatalf("空模型列表不应启用生图: %+v", got)
	}
	r.cfg.ImageModels = nil
	if got := r.imageGenerationConfig("send", "/tmp/run/token"); got != nil {
		t.Fatalf("nil ImageModels 不应启用生图: %+v", got)
	}
}
