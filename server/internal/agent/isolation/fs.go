package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// 目录布局的权限位。会话目录与各级根目录 0711:其他用户只能穿越、不能列
// 目录也不能增删条目——因此 root 持有的 meta.json 不会被会话身份删除或替换
// (目录写权限决定能否删/换文件)。workspace 带 setgid 且组可读写,ExecUID
// 经组权限访问(P5b)。
const (
	sessionDirPerm  = os.FileMode(0o711)
	privateDirPerm  = os.FileMode(0o700)
	privateFilePerm = os.FileMode(0o600)
	workspacePerm   = os.FileMode(0o770) | fs.ModeSetgid

	modeSpecialBits = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
)

// PrepareSession 按隔离布局创建或修正会话目录(幂等):
//
//	<sessionDir>/            root:root   0711
//	<sessionDir>/pi          PiUID:GID   0700
//	<sessionDir>/pi-agent    PiUID:GID   0700(令牌、models.json;ExecUID 不可读)
//	<sessionDir>/workspace   PiUID:GID   02770(setgid,组可读写)
//	<sessionDir>/meta.json   root:root   0600(存在时修正)
//
// 子目录属主与权限已正确时不遍历内容(workspace 可能很大);属主不对时才
// 递归 Lchown(不跟随符号链接)。同时确保父目录(会话根)为 root:root 0711,
// 否则会话身份无法穿越到达自己的目录。非 root 调用返回 ErrNotRoot。
func PrepareSession(sessionDir string, id Identity) error {
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}
	if sessionDir == "" {
		return errors.New("isolation: 会话目录为空")
	}
	sessionDir = filepath.Clean(sessionDir)
	if err := ensureRootDir(filepath.Dir(sessionDir)); err != nil {
		return err
	}
	if err := ensureRootDir(sessionDir); err != nil {
		return err
	}
	dirs := []struct {
		name string
		perm os.FileMode
	}{
		{"pi", privateDirPerm},
		{"pi-agent", privateDirPerm},
		{"workspace", workspacePerm},
	}
	for _, d := range dirs {
		if err := ensureOwnedDir(filepath.Join(sessionDir, d.name), id, d.perm); err != nil {
			return err
		}
	}
	if meta := filepath.Join(sessionDir, "meta.json"); pathExists(meta) {
		if err := fixRootFile(meta); err != nil {
			return err
		}
	}
	return nil
}

// PrepareDataDir 确保 Agent 数据目录与 sessions 根存在且为 root:root 0711
// (幂等):会话身份需要穿越这两级才能到达自己的目录,同时不能在数据目录
// 列目或写文件。非 root 调用返回 ErrNotRoot。
func PrepareDataDir(dataDir string) error {
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}
	if dataDir == "" {
		return errors.New("isolation: 数据目录为空")
	}
	if err := ensureRootDir(dataDir); err != nil {
		return err
	}
	return ensureRootDir(filepath.Join(dataDir, "sessions"))
}

// ChownFile 把 runner 以 root 写出的文件(models.json、令牌、xlyra.json、
// 系统提示等)移交给会话身份:PiUID:GID、0600。Lchown 不跟随符号链接;
// 符号链接本身不改权限位(chmod 会作用于链接目标)。
func ChownFile(path string, id Identity) error {
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("isolation: 查看 %s: %w", path, err)
	}
	if err := os.Lchown(path, int(id.PiUID), int(id.GID)); err != nil {
		return fmt.Errorf("isolation: 移交 %s: %w", path, err)
	}
	if fi.Mode().IsRegular() {
		if err := os.Chmod(path, privateFilePerm); err != nil {
			return fmt.Errorf("isolation: 收紧 %s 权限: %w", path, err)
		}
	}
	return nil
}

// Harden 去掉敏感路径上"组/其他"的所有权限位(目录 0700、文件 0600);
// 不存在的路径忽略,符号链接跳过(chmod 会跟随链接)。返回实际修改的路径,
// 供调用方记日志。
func Harden(paths []string) ([]string, error) {
	changed := []string{}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, fmt.Errorf("isolation: 查看 %s: %w", p, err)
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			continue
		}
		want := privateFilePerm
		if fi.IsDir() {
			want = privateDirPerm
		}
		if fi.Mode().Perm() == want && fi.Mode()&modeSpecialBits == 0 {
			continue
		}
		if err := os.Chmod(p, want); err != nil {
			return changed, fmt.Errorf("isolation: 加固 %s: %w", p, err)
		}
		changed = append(changed, p)
	}
	return changed, nil
}

// EnsureTraversable 给 root 自身以及 root 到 target 路径上的每一级目录
// (含 target)补上"其他用户可穿越"位(o+x):隔离后的会话身份需要穿越这些
// 目录才能到达 Agent 数据目录。只加 x 位、不放宽读/写,也不处理 root 之上
// 的祖先;target 不在 root 之下时仅处理 root;缺失的中间目录跳过
// (由调用方负责创建),路径上出现非目录时报错。
func EnsureTraversable(root, target string) error {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return errors.New("isolation: root 目录为空")
	}
	if err := addOtherTraverse(root); err != nil {
		return err
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return err // target 不在 root 之下:只保证 root 本身可穿越
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		if err := addOtherTraverse(filepath.Join(root, filepath.Join(parts[:i+1]...))); err != nil {
			return err
		}
	}
	return nil
}

// addOtherTraverse 在目录缺少"其他用户可穿越"位时补上 0o001,不改动其余
// 权限位;目录不存在时跳过。用 Stat 跟随符号链接,与 chmod 的语义一致。
func addOtherTraverse(dir string) error {
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("isolation: %s 不是目录", dir)
	}
	if fi.Mode().Perm()&0o001 != 0 {
		return nil
	}
	return os.Chmod(dir, fi.Mode().Perm()|0o001)
}

// ensureRootDir 保证目录存在且为 root:root 0711;不递归修改内容。
func ensureRootDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(dir, 0o711); err != nil {
			return err
		}
		if fi, err = os.Lstat(dir); err != nil {
			return err
		}
	}
	if !fi.IsDir() {
		return fmt.Errorf("isolation: %s 不是目录", dir)
	}
	if fi.Mode()&(os.ModePerm|modeSpecialBits) != sessionDirPerm {
		if err := os.Chmod(dir, sessionDirPerm); err != nil {
			return err
		}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && (st.Uid != 0 || st.Gid != 0) {
		if err := os.Lchown(dir, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

// ensureOwnedDir 保证子目录为 id 属主并带指定权限位(含 setgid);
// 属主不对时先递归 Lchown 整棵子树(不跟随符号链接)再修目录权限。
func ensureOwnedDir(dir string, id Identity, perm os.FileMode) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if fi, err = os.Lstat(dir); err != nil {
			return err
		}
	}
	if !fi.IsDir() {
		return fmt.Errorf("isolation: %s 不是目录", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("isolation: 无法读取 %s 的属主信息", dir)
	}
	if st.Uid != id.PiUID || st.Gid != id.GID {
		if err := lchownTree(dir, id.PiUID, id.GID); err != nil {
			return err
		}
	}
	if fi.Mode()&(os.ModePerm|modeSpecialBits) != perm {
		if err := os.Chmod(dir, perm); err != nil {
			return err
		}
	}
	return nil
}

// lchownTree 递归把 root 下所有条目(含自身)Lchown 成 uid:gid;
// filepath.Walk 用 Lstat 遍历,不跟随符号链接。
func lchownTree(root string, uid, gid uint32) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, int(uid), int(gid))
	})
}

// fixRootFile 把已存在的文件修正为 root:root 0600(如 meta.json)。
func fixRootFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && (st.Uid != 0 || st.Gid != 0) {
		if err := os.Lchown(path, 0, 0); err != nil {
			return err
		}
	}
	if fi.Mode().Perm() != privateFilePerm || fi.Mode()&modeSpecialBits != 0 {
		if err := os.Chmod(path, privateFilePerm); err != nil {
			return err
		}
	}
	return nil
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
