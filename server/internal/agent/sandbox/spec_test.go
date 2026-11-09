package sandbox

import (
	"strings"
	"testing"
)

func validSpec() Spec {
	return Spec{
		UID:       30100,
		GID:       30100,
		Workspace: "/tmp/ws",
		Tmp:       "/tmp/tmp",
		Net:       NetNone,
		Env:       []string{"PATH=/usr/bin:/bin", "HOME=/tmp/tmp"},
		Umask:     -1,
	}
}

// TestSpecValidateRejections 覆盖 fail-closed 的静态校验:每条非法 Spec 都要
// 被拒绝,而不是被静默放宽。
func TestSpecValidateRejections(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Spec)
		want string
	}{
		{"UID 0", func(s *Spec) { s.UID = 0 }, "UID 0"},
		{"UID 负数", func(s *Spec) { s.UID = -1 }, "非法身份"},
		{"GID 负数", func(s *Spec) { s.GID = -2 }, "非法身份"},
		{"Workspace 为空", func(s *Spec) { s.Workspace = "" }, "Workspace 为空"},
		{"Workspace 相对路径", func(s *Spec) { s.Workspace = "ws" }, "绝对路径"},
		{"Cwd 相对路径", func(s *Spec) { s.Cwd = "sub" }, "绝对路径"},
		{"Tmp 相对路径", func(s *Spec) { s.Tmp = "t" }, "绝对路径"},
		{"ReadOnly 相对路径", func(s *Spec) { s.ReadOnly = []string{"usr"} }, "绝对路径"},
		{"ReadWrite 相对路径", func(s *Spec) { s.ReadWrite = []string{"rw"} }, "绝对路径"},
		{"DevFiles 相对路径", func(s *Spec) { s.DevFiles = []string{"dev/null"} }, "绝对路径"},
		{"SystemRO 相对路径", func(s *Spec) { s.SystemRO = []string{"usr"} }, "绝对路径"},
		{"未知网络策略", func(s *Spec) { s.Net = "open" }, "未知网络策略"},
		{"空网络策略且非对照", func(s *Spec) { s.Net = "" }, "网络策略"},
		{"NetNone 加 SkipLandlock", func(s *Spec) { s.Control.SkipLandlock = true }, "互斥"},
		{"NetConnectPorts 加 SkipLandlock", func(s *Spec) {
			s.Net = NetConnectPorts
			s.ConnectPorts = []int{443}
			s.Control.SkipLandlock = true
		}, "互斥"},
		{"负数端口", func(s *Spec) { s.Net = NetConnectPorts; s.ConnectPorts = []int{-1} }, "非法端口"},
		{"端口 0", func(s *Spec) { s.Net = NetConnectPorts; s.ConnectPorts = []int{0} }, "非法端口"},
		{"端口超界", func(s *Spec) { s.Net = NetConnectPorts; s.ConnectPorts = []int{65536} }, "非法端口"},
		{"环境含 NUL", func(s *Spec) { s.Env = append(s.Env, "A=\x00b") }, "NUL"},
		{"环境缺等号", func(s *Spec) { s.Env = append(s.Env, "NOEQ") }, "KEY=VAL"},
		{"环境空键", func(s *Spec) { s.Env = append(s.Env, "=v") }, "KEY=VAL"},
		{"umask 超界", func(s *Spec) { s.Umask = 0o1000 }, "umask"},
		{"umask 小于 -1", func(s *Spec) { s.Umask = -2 }, "umask"},
		{"Timeout 负数", func(s *Spec) { s.Timeout = -1 }, "Timeout"},
		{"SkipNNP 但保留 seccomp", func(s *Spec) { s.Control.SkipNNP = true }, "no_new_privs"},
	}
	for _, tc := range cases {
		s := validSpec()
		tc.mut(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s:应被拒绝", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s:错误信息 %q 不含 %q", tc.name, err.Error(), tc.want)
		}
	}
}

// TestSpecValidAccepted:合法 Spec(含对照组合与空端口列表)必须通过。
func TestSpecValidAccepted(t *testing.T) {
	accept := []func(*Spec){
		func(*Spec) {},
		func(s *Spec) { s.Net = ""; s.Control.SkipLandlock = true }, // 正向对照
		func(s *Spec) { s.Control.SkipSeccomp = true },
		func(s *Spec) {
			s.Net = "" // 全对照:网络策略随之撤销
			s.Control.SkipLandlock, s.Control.SkipSeccomp, s.Control.SkipNNP = true, true, true
		},
		func(s *Spec) { s.Net = NetConnectPorts; s.ConnectPorts = nil }, // 空=全拒
		func(s *Spec) { s.Tmp = "" },                                    // Pi 模式可空
		func(s *Spec) { s.Limits = Limits{NProc: 8, CPU: 30, NoFile: 256, FSize: 1 << 20, AS: 1 << 30, Core: 0} },
	}
	for i, mut := range accept {
		s := validSpec()
		mut(&s)
		if err := s.Validate(); err != nil {
			t.Errorf("第 %d 个合法 Spec 被拒绝:%v", i, err)
		}
	}
}

// TestSpecRoundTrip:base64(JSON) 编码解码必须无损往返。
func TestSpecRoundTrip(t *testing.T) {
	s := validSpec()
	s.Net = NetConnectPorts
	s.ConnectPorts = []int{80, 443}
	s.ReadOnly = []string{"/opt/pi", "/etc/ssl"}
	s.ReadWrite = []string{"/data/x"}
	s.DevFiles = []string{"/dev/null"}
	s.SystemRO = []string{"/usr"}
	s.Limits = Limits{NProc: 8, Core: 0}
	s.Timeout = 120
	s.Control = Control{SkipSeccomp: true}
	enc, err := encodeSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "\x00\n ") {
		t.Fatalf("编码结果不能放进 argv:%q", enc)
	}
	got, err := decodeSpec(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != s.UID || got.GID != s.GID || got.Workspace != s.Workspace ||
		got.Tmp != s.Tmp || got.Cwd != s.Cwd || got.Net != s.Net ||
		got.Umask != s.Umask || got.Timeout != s.Timeout {
		t.Fatalf("往返后基本字段不符:%+v", got)
	}
	if len(got.ConnectPorts) != 2 || got.ConnectPorts[1] != 443 {
		t.Fatalf("端口不符:%v", got.ConnectPorts)
	}
	if len(got.ReadOnly) != 2 || len(got.ReadWrite) != 1 || len(got.DevFiles) != 1 || len(got.SystemRO) != 1 {
		t.Fatalf("路径列表不符:%+v", got)
	}
	if got.Limits != s.Limits || got.Control != s.Control || len(got.Env) != len(s.Env) {
		t.Fatalf("Limits/Control/Env 不符:%+v", got)
	}
}

// TestCheckChildSpecABITooOld:注入 ABI=3 模拟旧内核——带网络策略的 Spec
// 必须被 child 校验拒绝(不静默放行),无网络策略的对照 Spec 允许。
func TestCheckChildSpecABITooOld(t *testing.T) {
	netNone := validSpec()
	if err := checkChildSpec(netNone, 3); err == nil || !strings.Contains(err.Error(), "ABI") {
		t.Fatalf("NetNone 在 ABI=3 应被拒绝,得到 %v", err)
	}
	ports := validSpec()
	ports.Net = NetConnectPorts
	ports.ConnectPorts = []int{443}
	if err := checkChildSpec(ports, 3); err == nil || !strings.Contains(err.Error(), "ABI") {
		t.Fatalf("NetConnectPorts 在 ABI=3 应被拒绝,得到 %v", err)
	}
	// 无网络策略(仅对照)不受 ABI≥4 约束,文件系统只需 ABI≥1
	ctl := validSpec()
	ctl.Net = ""
	ctl.Control.SkipLandlock = true
	if err := checkChildSpec(ctl, 3); err != nil {
		t.Fatalf("对照 Spec 在 ABI=3 不应被拒绝:%v", err)
	}
	if err := checkChildSpec(validSpec(), 4); err != nil {
		t.Fatalf("ABI=4 应通过:%v", err)
	}
	if err := checkChildSpec(validSpec(), -1); err == nil {
		t.Fatal("Landlock 不可用时应拒绝")
	}
}

// TestResolveSystemRO:直接进 child 的 Spec(SystemRO 为 nil)取更严的
// 命令模式默认;显式列表完全替换。
func TestResolveSystemRO(t *testing.T) {
	got := resolveSystemRO(Spec{})
	if len(got) == 0 || got[0] != "/usr" {
		t.Fatalf("默认白名单异常:%v", got)
	}
	for _, p := range got {
		if p == "/etc" {
			t.Fatal("命令模式默认不应放行整个 /etc")
		}
	}
	custom := []string{"/usr", "/etc"}
	if got := resolveSystemRO(Spec{SystemRO: custom}); len(got) != 2 || got[1] != "/etc" {
		t.Fatalf("显式 SystemRO 应完全替换:%v", got)
	}
}
