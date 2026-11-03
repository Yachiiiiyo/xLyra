package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const detectMarker = "xlyra-iso-detect"

// Capability 是 uid 隔离能力的探测结果;OK=false 时 Reason 给出中文原因。
type Capability struct {
	OK     bool
	Reason string
}

// Detect 判断本进程能不能做 uid 隔离,只在数据目录里做临时文件探测、
// 不改既有数据:需要 Linux、root,且数据目录所在文件系统支持 chown 并强制
// Unix 权限位(macOS/Windows 的目录共享不强制权限位,会让隔离失效,
// 07-poc-results §十二.3)。
func Detect(dataDir string, c Config) Capability {
	c = c.Normalize()
	if os.Geteuid() != 0 {
		return Capability{Reason: fmt.Sprintf("需要 root 运行(当前 euid=%d)", os.Geteuid())}
	}
	if runtime.GOOS != "linux" {
		return Capability{Reason: "仅支持 Linux,当前系统 " + runtime.GOOS}
	}
	if dataDir == "" {
		return Capability{Reason: "未提供数据目录"}
	}
	if err := os.MkdirAll(dataDir, 0o711); err != nil {
		return Capability{Reason: "无法访问数据目录 " + dataDir + ":" + err.Error()}
	}
	// 用保留槽位的身份探测,不占用会话身份池
	id := identityForSlot(c, c.MaxSlots)
	dir, err := os.MkdirTemp(dataDir, ".isolation-detect-*")
	if err != nil {
		return Capability{Reason: "无法在数据目录创建临时探测目录:" + err.Error()}
	}
	defer os.RemoveAll(dir)
	// 探测身份需要穿越该目录读回文件
	if err := os.Chmod(dir, sessionDirPerm); err != nil {
		return Capability{Reason: "无法设置探测目录权限:" + err.Error()}
	}
	own := filepath.Join(dir, "own")
	if err := writeRootFile(own, detectMarker); err != nil {
		return Capability{Reason: err.Error()}
	}
	if err := os.Chown(own, int(id.PiUID), int(id.GID)); err != nil {
		return Capability{Reason: "数据目录所在文件系统不支持修改属主(可能是 macOS/Windows 的目录共享或缺少 CAP_CHOWN):" + err.Error()}
	}
	// chown 可能清掉权限位,补回
	_ = os.Chmod(own, 0o600)
	// 读回:目标 uid 必须能读到 chown 给它的文件
	if r := runProbe(context.Background(), id.PiCredential(), "/bin/grep -q "+detectMarker+" "+q(own)); r.code != 0 {
		return Capability{Reason: fmt.Sprintf("以探测身份(uid=%d)读回数据目录文件失败(exit=%d%s)", id.PiUID, r.code, errSuffix(r))}
	}
	// 权限位必须被文件系统强制:root 的 0600 文件,探测身份必须读不到
	rootOnly := filepath.Join(dir, "root-only")
	if err := writeRootFile(rootOnly, detectMarker); err != nil {
		return Capability{Reason: err.Error()}
	}
	if r := runProbe(context.Background(), id.PiCredential(), "/bin/grep -q "+detectMarker+" "+q(rootOnly)); r.code == 0 {
		return Capability{Reason: "数据目录所在文件系统不强制 Unix 权限位(可能是 macOS/Windows 的目录共享),uid 隔离会失效"}
	}
	return Capability{OK: true}
}

// writeRootFile 以 root 身份写一个 0600 探测文件(显式 chmod,防 umask 收紧)。
func writeRootFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("无法写入探测文件 %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}
