package isolation

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// requireIntegration 集成测试需要 Linux 容器内以 root 运行,
// 并显式设置 ISOLATION_INTEGRATION=1;其余环境跳过。
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("ISOLATION_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + ISOLATION_INTEGRATION=1")
	}
}

func statMode(t *testing.T, p string) (os.FileMode, uint32, uint32) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("无法读取 %s 的 stat_t", p)
	}
	return fi.Mode(), st.Uid, st.Gid
}

func logReport(t *testing.T, rep Report) {
	t.Helper()
	for _, c := range rep.Checks {
		t.Logf("%s passed=%v detail=%s", c.Name, c.Passed, c.Detail)
	}
}

func requireFailed(t *testing.T, rep Report, name string) {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			if c.Passed {
				t.Fatalf("检查项 %q 预期失败但通过了", name)
			}
			return
		}
	}
	t.Fatalf("报告中没有检查项 %q", name)
}

// TestPrepareSessionLayout 逐项断言 PrepareSession 产出的属主/权限位
// (含 setgid、幂等、修复、meta.json canary 不可删改)。
func TestPrepareSessionLayout(t *testing.T) {
	requireIntegration(t)
	tmp := t.TempDir()
	id, err := IdentityFor(Config{}, 7)
	if err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(tmp, "sess-a")
	if err := PrepareSession(sess, id); err != nil {
		t.Fatal(err)
	}

	// 父目录(会话根)与会话目录本身:root:root 0711
	for _, p := range []string{tmp, sess} {
		m, uid, gid := statMode(t, p)
		if m.Perm() != 0o711 || uid != 0 || gid != 0 {
			t.Fatalf("%s 应为 root:root 0711,得到 uid=%d gid=%d mode=%o", p, uid, gid, m.Perm())
		}
	}
	// pi/、pi-agent/:PiUID:GID 0700 且无 setgid
	for _, name := range []string{"pi", "pi-agent"} {
		m, uid, gid := statMode(t, filepath.Join(sess, name))
		if m.Perm() != 0o700 || m&modeSpecialBits != 0 || uid != id.PiUID || gid != id.GID {
			t.Fatalf("%s 应为 %d:%d 0700,得到 uid=%d gid=%d mode=%o", name, id.PiUID, id.GID, uid, gid, m.Perm())
		}
	}
	// workspace/:PiUID:GID 02770(setgid)
	m, uid, gid := statMode(t, filepath.Join(sess, "workspace"))
	if m.Perm() != 0o770 || m&os.ModeSetgid == 0 || uid != id.PiUID || gid != id.GID {
		t.Fatalf("workspace 应为 %d:%d 02770,得到 uid=%d gid=%d mode=%o", id.PiUID, id.GID, uid, gid, m.Perm())
	}

	// meta.json:存在但属主/权限不对时被修正为 root:root 0600
	meta := filepath.Join(sess, "meta.json")
	if err := os.WriteFile(meta, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(meta, 1234, 1234); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSession(sess, id); err != nil {
		t.Fatal(err)
	}
	m, uid, gid = statMode(t, meta)
	if m.Perm() != 0o600 || uid != 0 || gid != 0 {
		t.Fatalf("meta.json 应为 root:root 0600,得到 uid=%d gid=%d mode=%o", uid, gid, m.Perm())
	}

	// 修复:workspace 属主被改坏且里面留下 root 的文件
	ws := filepath.Join(sess, "workspace")
	stray := filepath.Join(ws, "root-file")
	if err := os.WriteFile(stray, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(ws, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ws, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSession(sess, id); err != nil {
		t.Fatal(err)
	}
	m, uid, gid = statMode(t, ws)
	if m.Perm() != 0o770 || m&os.ModeSetgid == 0 || uid != id.PiUID || gid != id.GID {
		t.Fatalf("修复后 workspace 应为 %d:%d 02770,得到 uid=%d gid=%d mode=%o", id.PiUID, id.GID, uid, gid, m.Perm())
	}
	_, uid, gid = statMode(t, stray)
	if uid != id.PiUID || gid != id.GID {
		t.Fatalf("修复后 workspace 内文件应 Lchown 给会话身份,得到 uid=%d gid=%d", uid, gid)
	}

	// meta.json canary:A 的 PiUID 与 ExecUID 都读不了、删不了、改不了名
	if err := os.WriteFile(meta, []byte("canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, cred := range []*syscall.Credential{id.PiCredential(), id.ExecCredential()} {
		if r := runProbe(ctx, cred, "/bin/rm "+q(meta)); r.code == 0 {
			t.Fatal("会话身份不应能删除 meta.json")
		}
		if r := runProbe(ctx, cred, "/bin/mv "+q(meta)+" "+q(meta+".r")); r.code == 0 {
			t.Fatal("会话身份不应能改名 meta.json")
		}
		if r := runProbe(ctx, cred, "/bin/cat "+q(meta)); r.code == 0 {
			t.Fatal("会话身份不应能读取 meta.json")
		}
	}
}

// TestPrepareDataDirLayout 验证 PrepareDataDir 创建与修正 dataDir/sessions 的
// 布局(存在但权限/属主不对时修正,幂等)。
func TestPrepareDataDirLayout(t *testing.T) {
	requireIntegration(t)
	dataDir := filepath.Join(t.TempDir(), "agent")
	if err := PrepareDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dataDir, filepath.Join(dataDir, "sessions")} {
		m, uid, gid := statMode(t, p)
		if m.Perm() != 0o711 || uid != 0 || gid != 0 {
			t.Fatalf("%s 应为 root:root 0711,得到 uid=%d gid=%d mode=%o", p, uid, gid, m.Perm())
		}
	}

	// 已存在但被改坏:属主与权限都被修正,内容不动。
	sessions := filepath.Join(dataDir, "sessions")
	stray := filepath.Join(sessions, "keep")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	m, uid, gid := statMode(t, sessions)
	if m.Perm() != 0o711 || uid != 0 || gid != 0 {
		t.Fatalf("修正后 sessions 应为 root:root 0711,得到 uid=%d gid=%d mode=%o", uid, gid, m.Perm())
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("已有内容不应被动到: %v", err)
	}
}

// selfCheckInput 搭一套带敏感路径与可读探针的数据目录。
func selfCheckInput(t *testing.T) (string, SelfCheckInput) {
	t.Helper()
	tmp := t.TempDir()
	// t.TempDir 及其祖先(Test<名字><数字>/001)都是 0700;探针 uid 需要一路穿越到数据目录
	makeTraversable(t, tmp)
	dataDir := filepath.Join(tmp, "agent")
	if err := os.MkdirAll(filepath.Join(dataDir, "sessions"), 0o711); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(tmp, "conf")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(conf, "master.key")
	if err := os.WriteFile(secret, []byte("integration-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(tmp, "db")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(tmp, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probeFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(probeFile, []byte("# demo"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir, SelfCheckInput{
		DataDir:       dataDir,
		SecretPaths:   []string{secret, conf, db, filepath.Join(conf, "missing.key")},
		ReadableProbe: probeFile,
		SelfPID:       os.Getpid(),
	}
}

// TestSelfCheckOK 正确布局下自检全绿,Detect 可用。
func TestSelfCheckOK(t *testing.T) {
	requireIntegration(t)
	dataDir, in := selfCheckInput(t)
	rep := SelfCheck(context.Background(), Config{}, in)
	logReport(t, rep)
	if !rep.OK {
		t.Fatal("正确布局下自检应通过")
	}
	if len(rep.Checks) != 8 {
		t.Fatalf("应 8 项检查,得到 %d", len(rep.Checks))
	}
	cap := Detect(dataDir, Config{})
	if !cap.OK {
		t.Fatalf("root+Linux 下 Detect 应 OK: %s", cap.Reason)
	}
}

// TestSelfCheckSecretBroken 敏感文件被放宽到 0644 且所在目录可穿越时应被发现。
// 只放宽文件、目录仍是 0700 时,其他用户进不去,读不到才是对的(下面的子用例验证自检不误报)。
func TestSelfCheckSecretBroken(t *testing.T) {
	requireIntegration(t)
	t.Run("文件与目录都放宽", func(t *testing.T) {
		_, in := selfCheckInput(t)
		secret := in.SecretPaths[0]
		if err := os.Chmod(secret, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(secret), 0o755); err != nil {
			t.Fatal(err)
		}
		rep := SelfCheck(context.Background(), Config{}, in)
		logReport(t, rep)
		if rep.OK {
			t.Fatal("敏感文件 0644 且目录可穿越时自检不应通过")
		}
		requireFailed(t, rep, checkSecrets)
	})
	t.Run("只放宽文件目录仍封闭则不误报", func(t *testing.T) {
		_, in := selfCheckInput(t)
		if err := os.Chmod(in.SecretPaths[0], 0o644); err != nil {
			t.Fatal(err)
		}
		rep := SelfCheck(context.Background(), Config{}, in)
		logReport(t, rep)
		if !rep.OK {
			t.Fatal("目录 0700 时其他用户进不去,自检应通过")
		}
	})
}

// TestSelfCheckWritableRoots 数据目录根或 sessions 根可写时应被发现。
func TestSelfCheckWritableRoots(t *testing.T) {
	requireIntegration(t)
	t.Run("数据目录根", func(t *testing.T) {
		dataDir, in := selfCheckInput(t)
		if err := os.Chmod(dataDir, 0o777); err != nil {
			t.Fatal(err)
		}
		rep := SelfCheck(context.Background(), Config{}, in)
		logReport(t, rep)
		if rep.OK {
			t.Fatal("数据目录 0777 时自检不应通过")
		}
		requireFailed(t, rep, checkDataRO)
	})
	t.Run("会话根", func(t *testing.T) {
		dataDir, in := selfCheckInput(t)
		if err := os.Chmod(filepath.Join(dataDir, "sessions"), 0o777); err != nil {
			t.Fatal(err)
		}
		rep := SelfCheck(context.Background(), Config{}, in)
		logReport(t, rep)
		if rep.OK {
			t.Fatal("sessions 0777 时自检不应通过")
		}
		requireFailed(t, rep, checkDataRO)
	})
}

// TestSelfCheckReadableProbeMissing 公共可读探针缺失时 fail-closed。
func TestSelfCheckReadableProbeMissing(t *testing.T) {
	requireIntegration(t)
	_, in := selfCheckInput(t)
	in.ReadableProbe = filepath.Join(in.DataDir, "nope.md")
	rep := SelfCheck(context.Background(), Config{}, in)
	logReport(t, rep)
	if rep.OK {
		t.Fatal("可读探针缺失时自检不应通过")
	}
	requireFailed(t, rep, checkPublic)
}

// TestSelfCheckDetectsBrokenLayout 故意破坏自检环境的布局,证明探测套件
// 真的能发现问题(SelfCheck 的环境是随机名且随调用创建/销毁,外部无法在
// 运行中改它,这里直接复用同一套 buildCheckEnv/runChecks 代码路径)。
func TestSelfCheckDetectsBrokenLayout(t *testing.T) {
	requireIntegration(t)
	scenarios := []struct {
		name   string
		breakE func(e *checkEnv)
		check  string
	}{
		{"B 工作区 0777", func(e *checkEnv) { _ = os.Chmod(e.bWS, 0o777) }, checkCross},
		{"会话目录归 A", func(e *checkEnv) {
			_ = os.Chown(e.aDir, int(e.a.PiUID), int(e.a.GID))
		}, checkMeta},
		{"会话目录 0777", func(e *checkEnv) { _ = os.Chmod(e.aDir, 0o777) }, checkMeta},
		{"pi-agent 0770", func(e *checkEnv) { _ = os.Chmod(e.aAgent, 0o770) }, checkExec},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			tmp := t.TempDir()
			makeTraversable(t, tmp)
			dataDir := filepath.Join(tmp, "agent")
			if err := os.MkdirAll(filepath.Join(dataDir, "sessions"), 0o711); err != nil {
				t.Fatal(err)
			}
			e, err := buildCheckEnv(dataDir, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer e.destroy()
			sc.breakE(e)
			rep := runChecks(context.Background(), e, SelfCheckInput{DataDir: dataDir, SelfPID: os.Getpid()})
			logReport(t, rep)
			if rep.OK {
				t.Fatal("布局被破坏后自检不应通过")
			}
			requireFailed(t, rep, sc.check)
		})
	}
}

// makeTraversable 把 dir 及其到系统临时目录之间的所有祖先放宽到 0711(只给"穿越"权限),
// 否则探针 uid 走不到自己的目录,正向对照项就会(正确地)判失败。
func makeTraversable(t *testing.T, dir string) {
	t.Helper()
	stop := filepath.Clean(os.TempDir())
	for cur := filepath.Clean(dir); cur != stop && cur != string(filepath.Separator) && cur != "."; cur = filepath.Dir(cur) {
		if err := os.Chmod(cur, 0o711); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEnsureTraversableProbe 集成风格验证:收紧到 root-only 的多层目录,
// EnsureTraversable 后低权身份能穿越到达目标、但读不到路径上的 0600 文件。
func TestEnsureTraversableProbe(t *testing.T) {
	requireIntegration(t)
	tmp := t.TempDir()
	makeTraversable(t, tmp)
	root := filepath.Join(tmp, "work")
	mid := filepath.Join(root, "a")
	leaf := filepath.Join(mid, "b", "agent")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{root, filepath.Join(mid, "b")} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(mid, "secret.key")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(leaf, "canary")
	if err := os.WriteFile(canary, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := IdentityFor(Config{}, 1)
	if err != nil {
		t.Fatal(err)
	}

	if r := runProbe(context.Background(), id.PiCredential(), "/bin/cat "+q(canary)); r.code == 0 {
		t.Fatal("收紧后低权身份不应能到达目标(前置对照)")
	}
	if err := EnsureTraversable(root, leaf); err != nil {
		t.Fatal(err)
	}
	if r := runProbe(context.Background(), id.PiCredential(), "/bin/cat "+q(canary)); r.code != 0 {
		t.Fatalf("EnsureTraversable 后低权身份应能穿越到达目标: exit=%d%s", r.code, errSuffix(r))
	}
	if r := runProbe(context.Background(), id.PiCredential(), "/bin/cat "+q(secret)); r.code == 0 {
		t.Fatal("路径上 root 的 0600 文件不应被读到")
	}
}
