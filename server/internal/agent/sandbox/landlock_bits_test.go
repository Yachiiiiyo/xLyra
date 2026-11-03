package sandbox

import "testing"

// TestFSAccessForABI:访问位必须按 ABI 填全且单调不减(没放进
// handled_access_fs 的权限不受 Landlock 限制)。
func TestFSAccessForABI(t *testing.T) {
	const abi1 = 0x1FFF // ABI1 的 13 个基本位
	want := map[int]uint64{
		1: abi1,
		2: abi1 | accessFSRefer,
		3: abi1 | accessFSRefer | accessFSTruncate,
		4: abi1 | accessFSRefer | accessFSTruncate,
		5: abi1 | accessFSRefer | accessFSTruncate | accessFSIoctlDev,
		9: abi1 | accessFSRefer | accessFSTruncate | accessFSIoctlDev | accessFSResolveUnix,
	}
	for abi := 1; abi <= 10; abi++ {
		got := fsAccessForABI(abi)
		if abi > 1 && got < fsAccessForABI(abi-1) {
			t.Fatalf("ABI %d 的访问位比 ABI %d 少", abi, abi-1)
		}
		if w, ok := want[abi]; ok && got != w {
			t.Fatalf("ABI %d 期望 %#x,得到 %#x", abi, w, got)
		}
		if got & ^uint64(0x1FFFF) != 0 {
			t.Fatalf("ABI %d 出现超出已知范围的位:%#x", abi, got)
		}
	}
	if fsAccessForABI(8)&accessFSResolveUnix != 0 {
		t.Fatal("ABI 8 不应包含 RESOLVE_UNIX")
	}
	if fsAccessForABI(10)&accessFSResolveUnix == 0 {
		t.Fatal("ABI 10 应包含 RESOLVE_UNIX")
	}
	if fsAccessForABI(0) != abi1 {
		t.Fatal("ABI 0 应与 ABI 1 一致(基本位)")
	}
}
