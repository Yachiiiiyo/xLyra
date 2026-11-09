package sandbox

import "testing"

// TestBuildSeccompFilterStructure 在任意平台上检查两种架构的过滤器结构:
// 架构核对在前(未知架构一律 EPERM)、禁用表逐项 EPERM、NetNone 时 socket()
// 只放行 AF_UNIX。不安装,纯构造。
func TestBuildSeccompFilterStructure(t *testing.T) {
	cases := []struct {
		goarch   string
		audit    uint32
		socketNr uint32
	}{
		{"arm64", 0xc00000b7, 198},
		{"amd64", 0xc000003e, 41},
	}
	for _, tc := range cases {
		f, err := buildSeccompFilter(tc.goarch, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.goarch, err)
		}
		if len(f) != 4+2*14+1 {
			t.Fatalf("%s: 过滤器长度 %d,期望 %d", tc.goarch, len(f), 4+2*14+1)
		}
		if f[0] != (sockFilter{Code: bpfLDWABS, K: 4}) {
			t.Fatalf("%s: 首条应为加载 arch:%v", tc.goarch, f[0])
		}
		if f[1] != (sockFilter{Code: bpfJmpJEQK, Jt: 1, K: tc.audit}) {
			t.Fatalf("%s: 审计架构常量不符:%v", tc.goarch, f[1])
		}
		if f[2] != (sockFilter{Code: bpfRetK, K: seccompRetErrno | errnoEPERM}) {
			t.Fatalf("%s: 未知架构应 EPERM:%v", tc.goarch, f[2])
		}
		if f[3] != (sockFilter{Code: bpfLDWABS, K: 0}) {
			t.Fatalf("%s: 第四条应为加载 nr:%v", tc.goarch, f[3])
		}
		ep := sockFilter{Code: bpfRetK, K: seccompRetErrno | errnoEPERM}
		for i, nr := range filterArchs[tc.goarch].denied {
			jmp := sockFilter{Code: bpfJmpJEQK, Jf: 1, K: nr}
			if f[4+2*i] != jmp || f[5+2*i] != ep {
				t.Fatalf("%s: 禁用表第 %d 项不符:%v %v", tc.goarch, i, f[4+2*i], f[5+2*i])
			}
		}
		if f[len(f)-1] != (sockFilter{Code: bpfRetK, K: seccompRetAllow}) {
			t.Fatalf("%s: 末条应为 ALLOW:%v", tc.goarch, f[len(f)-1])
		}

		// NetNone:socket() 的 domain != AF_UNIX 一律 EPERM
		f, err = buildSeccompFilter(tc.goarch, true)
		if err != nil {
			t.Fatalf("%s: %v", tc.goarch, err)
		}
		socketBlock := f[len(f)-5:]
		if socketBlock[0] != (sockFilter{Code: bpfJmpJEQK, Jf: 3, K: tc.socketNr}) {
			t.Fatalf("%s: socket 判断的调用号不符:%v", tc.goarch, socketBlock[0])
		}
		if socketBlock[1] != (sockFilter{Code: bpfLDWABS, K: 16}) {
			t.Fatalf("%s: 应加载 args[0]:%v", tc.goarch, socketBlock[1])
		}
		if socketBlock[2] != (sockFilter{Code: bpfJmpJEQK, Jt: 1, K: afUnix}) {
			t.Fatalf("%s: AF_UNIX 判断不符:%v", tc.goarch, socketBlock[2])
		}
		if socketBlock[3] != ep || socketBlock[4] != (sockFilter{Code: bpfRetK, K: seccompRetAllow}) {
			t.Fatalf("%s: socket 块结尾不符:%v", tc.goarch, socketBlock[3:])
		}
	}
	if _, err := buildSeccompFilter("386", false); err == nil {
		t.Fatal("未知架构应报错")
	}
}
