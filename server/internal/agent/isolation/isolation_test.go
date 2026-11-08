package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalize 验证零值补默认、已设值保留。
func TestNormalize(t *testing.T) {
	got := Config{}.Normalize()
	if got.UIDBase != 20000 || got.MaxSlots != 4000 {
		t.Fatalf("零值应取默认 20000/4000,得到 %d/%d", got.UIDBase, got.MaxSlots)
	}
	got = Config{UIDBase: 30000, MaxSlots: 10}.Normalize()
	if got.UIDBase != 30000 || got.MaxSlots != 10 {
		t.Fatalf("已设值不应被覆盖,得到 %d/%d", got.UIDBase, got.MaxSlots)
	}
	got = Config{UIDBase: -1, MaxSlots: -5}.Normalize()
	if got.UIDBase != 20000 || got.MaxSlots != 4000 {
		t.Fatalf("负值应取默认,得到 %d/%d", got.UIDBase, got.MaxSlots)
	}
}

// TestIdentityFor 验证身份公式、槽位边界与保留槽位。
func TestIdentityFor(t *testing.T) {
	c := Config{}
	id, err := IdentityFor(c, 1)
	if err != nil {
		t.Fatal(err)
	}
	if id.Slot != 1 || id.PiUID != 20002 || id.ExecUID != 20003 || id.GID != 20002 {
		t.Fatalf("槽位 1 身份不符: %+v", id)
	}
	id, err = IdentityFor(c, 2)
	if err != nil || id.PiUID != 20004 || id.ExecUID != 20005 || id.GID != 20004 {
		t.Fatalf("槽位 2 身份不符: %+v err=%v", id, err)
	}
	// 边界:最大可分配槽位是 MaxSlots-2
	if _, err := IdentityFor(c, 3998); err != nil {
		t.Fatalf("槽位 3998 应可分配: %v", err)
	}
	// 保留槽位与越界
	for _, slot := range []int{0, -1, 3999, 4000, 4001, 99999} {
		if _, err := IdentityFor(c, slot); err == nil {
			t.Fatalf("槽位 %d 应返回错误", slot)
		}
	}
	// uid 超出 int32:UIDBase 本身越界、或加上池上限后越界
	for _, bad := range []Config{
		{UIDBase: 1 << 31, MaxSlots: 100},
		{UIDBase: 2147483000, MaxSlots: 4000},
	} {
		if _, err := IdentityFor(bad, 1); err == nil {
			t.Fatalf("配置 %+v 应因 uid 越界返回错误", bad)
		}
	}
}

// TestAllocatableSlots 验证可分配槽数(MaxSlots-2)。
func TestAllocatableSlots(t *testing.T) {
	if n := (Config{}).AllocatableSlots(); n != 3998 {
		t.Fatalf("默认应 3998,得到 %d", n)
	}
	if n := (Config{MaxSlots: 4}).AllocatableSlots(); n != 2 {
		t.Fatalf("MaxSlots=4 应 2,得到 %d", n)
	}
}

// TestSelectSlot 覆盖空表、部分占用、跳号、全满与小池。
func TestSelectSlot(t *testing.T) {
	c := Config{}
	if s, err := SelectSlot(c, nil); err != nil || s != 1 {
		t.Fatalf("空表应选 1,得到 %d err=%v", s, err)
	}
	if s, err := SelectSlot(c, map[int]bool{1: true}); err != nil || s != 2 {
		t.Fatalf("占用 1 应选 2,得到 %d err=%v", s, err)
	}
	if s, err := SelectSlot(c, map[int]bool{1: true, 2: true, 4: true}); err != nil || s != 3 {
		t.Fatalf("占用 1,2,4 应选 3,得到 %d err=%v", s, err)
	}
	// used 里的保留槽位/垃圾键不影响分配
	if s, err := SelectSlot(c, map[int]bool{0: true, 3999: true, 4000: true}); err != nil || s != 1 {
		t.Fatalf("保留槽位不应计入占用,得到 %d err=%v", s, err)
	}
	full := make(map[int]bool, 3998)
	for i := 1; i <= 3998; i++ {
		full[i] = true
	}
	if _, err := SelectSlot(c, full); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("全满应返回 ErrNoSlot,得到 %v", err)
	}
	small := Config{MaxSlots: 4}
	if _, err := SelectSlot(small, map[int]bool{1: true, 2: true}); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("小池全满应返回 ErrNoSlot,得到 %v", err)
	}
}

// TestCredentials 验证两种凭据的字段。
func TestCredentials(t *testing.T) {
	id := Identity{Slot: 1, PiUID: 20002, ExecUID: 20003, GID: 20002}
	pc := id.PiCredential()
	if pc.Uid != 20002 || pc.Gid != 20002 || len(pc.Groups) != 1 || pc.Groups[0] != 20002 {
		t.Fatalf("Pi 凭据不符: %+v", pc)
	}
	ec := id.ExecCredential()
	if ec.Uid != 20003 || ec.Gid != 20002 || len(ec.Groups) != 1 || ec.Groups[0] != 20002 {
		t.Fatalf("Exec 凭据不符: %+v", ec)
	}
}

// TestDetectNotRoot 非 root 下应 fail-closed 且原因提到 root。
func TestDetectNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过")
	}
	cap := Detect(t.TempDir(), Config{})
	if cap.OK {
		t.Fatal("非 root 下 Detect 不应 OK")
	}
	if !strings.Contains(cap.Reason, "root") {
		t.Fatalf("原因应包含 root: %q", cap.Reason)
	}
}

// TestPrepareSessionNotRoot 非 root 应返回 ErrNotRoot 且不做任何文件操作。
func TestPrepareSessionNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过")
	}
	id, err := IdentityFor(Config{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareSession(filepath.Join(t.TempDir(), "s"), id); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("应返回 ErrNotRoot,得到 %v", err)
	}
}

// TestChownFileNotRoot 非 root 应返回 ErrNotRoot。
func TestChownFileNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过")
	}
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ChownFile(f, Identity{}); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("应返回 ErrNotRoot,得到 %v", err)
	}
}

// TestPrepareDataDirNotRoot 非 root 应返回 ErrNotRoot 且不创建任何目录。
func TestPrepareDataDirNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过")
	}
	dataDir := filepath.Join(t.TempDir(), "agent")
	if err := PrepareDataDir(dataDir); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("应返回 ErrNotRoot,得到 %v", err)
	}
	if _, err := os.Lstat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("非 root 下不应创建目录: %v", err)
	}
}

// TestPrepareWorkspaceNotRoot 非 root 应返回 ErrNotRoot 且不创建任何目录。
func TestPrepareWorkspaceNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("本用例验证非 root 行为,root 环境跳过")
	}
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := PrepareWorkspace(dir); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("应返回 ErrNotRoot,得到 %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("非 root 下不应创建目录: %v", err)
	}
}

// TestPrepareWorkspaceRoot(root 才能跑)验证目录被创建且不依赖 umask 地为 0777。
func TestPrepareWorkspaceRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root,非 root 环境跳过")
	}
	dir := filepath.Join(t.TempDir(), "workspace", "sub")
	if err := PrepareWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o777 {
		t.Fatalf("权限 = %o,应为 777", fi.Mode().Perm())
	}
	// 幂等:再次调用不报错。
	if err := PrepareWorkspace(dir); err != nil {
		t.Fatal(err)
	}
}

// TestHarden 验证去权限位、返回修改列表、忽略缺失路径、跳过符号链接。
func TestHarden(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	sgid := filepath.Join(tmp, "sgid")
	if err := os.Mkdir(sgid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sgid, 0o2770); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(tmp, "f1")
	if err := os.WriteFile(f1, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f1, 0o640); err != nil {
		t.Fatal(err)
	}
	f2 := filepath.Join(tmp, "f2") // 已是 0600,不应出现在修改列表
	if err := os.WriteFile(f2, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink("f1", link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(tmp, "missing")

	changed, err := Harden([]string{dir, sgid, f1, f2, missing, link})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{dir, sgid, f1}
	if len(changed) != len(want) {
		t.Fatalf("修改列表应为 %v,得到 %v", want, changed)
	}
	for i := range want {
		if changed[i] != want[i] {
			t.Fatalf("修改列表应为 %v,得到 %v", want, changed)
		}
	}
	for _, p := range []struct {
		path string
		perm os.FileMode
	}{
		{dir, 0o700},
		{sgid, 0o700},
		{f1, 0o600},
		{f2, 0o600},
	} {
		fi, err := os.Lstat(p.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != p.perm {
			t.Fatalf("%s 权限应为 %o,得到 %o", p.path, p.perm, fi.Mode().Perm())
		}
	}
	if fi, err := os.Lstat(sgid); err == nil && fi.Mode()&os.ModeSetgid != 0 {
		t.Fatal("Harden 应清掉目录的 setgid 位")
	}
	// 符号链接未被改动(仍存在,目标权限未被 Harden 触碰)
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("符号链接不应被处理: %v", err)
	}
}

// permOf 返回 Lstat 权限位。
func permOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return fi.Mode().Perm()
}

// TestEnsureTraversable 验证只补 o+x、不碰无关目录、不改读写位、
// target 在 root 外只处理 root、root 为文件时报错。
func TestEnsureTraversable(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "work")
	mid := filepath.Join(root, "a")
	leaf := filepath.Join(mid, "b")
	for _, d := range []string{root, mid, leaf} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(mid, 0o740); err != nil { // 其他用户无任何位
		t.Fatal(err)
	}
	secret := filepath.Join(mid, "secret.key") // 中间的文件不应被改
	if err := os.WriteFile(secret, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other") // 路径外的兄弟目录不应被改
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(leaf, "agent")
	if err := EnsureTraversable(root, target); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want os.FileMode
	}{
		{root, 0o701},
		{mid, 0o741},
		{leaf, 0o701},
		{other, 0o700},
	} {
		if got := permOf(t, c.path); got != c.want {
			t.Fatalf("%s 权限应为 %o,得到 %o", c.path, c.want, got)
		}
	}
	if got := permOf(t, secret); got != 0o600 {
		t.Fatalf("路径上的文件不应被改动,得到 %o", got)
	}

	// 幂等:重复执行权限不变
	if err := EnsureTraversable(root, target); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, root); got != 0o701 {
		t.Fatalf("重复执行后 root 权限应为 0701,得到 %o", got)
	}

	// target 即 root 本身:只处理 root
	if err := EnsureTraversable(root, root); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, mid); got != 0o741 {
		t.Fatalf("target 为 root 时不应改动子目录,得到 %o", got)
	}

	// target 在 root 之外:仅 root 自身被处理,不碰 root 的祖先
	outside := filepath.Join(tmp, "elsewhere")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTraversable(root, outside); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, outside); got != 0o700 {
		t.Fatalf("root 外的 target 不应被改动,得到 %o", got)
	}
	if got := permOf(t, mid); got != 0o741 {
		t.Fatalf("target 在外时不应改动 root 子目录,得到 %o", got)
	}

	// 路径上出现普通文件:报错
	blocked := filepath.Join(root, "file-as-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTraversable(root, filepath.Join(blocked, "x")); err == nil {
		t.Fatal("路径上有普通文件时应报错")
	}

	// root 为空报错
	if err := EnsureTraversable("", tmp); err == nil {
		t.Fatal("root 为空应报错")
	}
}

// 非 root 调用 RelaxWorkspacePermissions 返回 ErrNotRoot,且不改动任何权限。
func TestRelaxWorkspacePermissionsNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 下走实际放开权限的测试")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := RelaxWorkspacePermissions(dir); !errors.Is(err, ErrNotRoot) {
		t.Fatalf("err = %v, want ErrNotRoot", err)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
}

// root 下:已有目录/文件被放开读写,可执行位保留,符号链接指向的目标不被改动。
func TestRelaxWorkspacePermissionsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(sub, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := RelaxWorkspacePermissions(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(sub); info.Mode().Perm() != 0o777 {
		t.Fatalf("dir mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(script); info.Mode().Perm() != 0o777 {
		t.Fatalf("exec file mode = %v, want executable and writable", info.Mode().Perm())
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() != 0o600 {
		t.Fatalf("symlink target must not change, got %v", info.Mode().Perm())
	}
}
