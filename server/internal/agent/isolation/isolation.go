// Package isolation 实现 Agent 子进程的 uid/gid 隔离基础(08-implementation-
// plan §八 P5a、§十六 16.4 L1):xLyra 以 root 运行,按整数"槽位"为每个会话
// 确定性分配一组非特权身份(PiUID/ExecUID/私有 GID),把会话目录权限收紧到
// root 与该身份之外谁都碰不到;Detect/SelfCheck 在实际数据目录上做 fail-closed
// 自检,任一"该失败的没失败"即判定隔离不可用,由调用方关闭 Agent 敏感能力。
// 本包只做身份、目录权限与自检,不改 runner/pirpc/session。
package isolation

import (
	"errors"
	"fmt"
	"math"
	"syscall"
)

const (
	defaultUIDBase  = 20000
	defaultMaxSlots = 4000
)

var (
	// ErrNotRoot 表示当前进程不是 root,无法做属主/权限操作。
	ErrNotRoot = errors.New("isolation: 需要 root 运行")
	// ErrNoSlot 表示身份池已无空闲槽位。
	ErrNoSlot = errors.New("isolation: 身份池已满,无空闲槽位")
)

// Config 是身份池参数,零值字段取默认(UIDBase=20000、MaxSlots=4000)。
type Config struct {
	UIDBase  int
	MaxSlots int
}

// Normalize 补全零值字段。
func (c Config) Normalize() Config {
	if c.UIDBase <= 0 {
		c.UIDBase = defaultUIDBase
	}
	if c.MaxSlots <= 0 {
		c.MaxSlots = defaultMaxSlots
	}
	return c
}

// Identity 是一个会话的专属身份。GID 与 PiUID 相同(每会话私有组);
// ExecUID 同属该组但不与 Pi 同 uid,命令进程经组权限读写工作区(P5b),
// 同时读不到 Pi 的私有目录、也不能 ptrace/读 Pi 的 /proc。
type Identity struct {
	Slot    int
	PiUID   uint32
	ExecUID uint32
	GID     uint32
}

// IdentityFor 返回槽位 slot 对应的身份。可分配范围是 1..MaxSlots-2;
// MaxSlots-1 与 MaxSlots 两个槽位保留给启动自检,不分配给会话。
func IdentityFor(c Config, slot int) (Identity, error) {
	c = c.Normalize()
	if int64(c.UIDBase) > math.MaxInt32 || int64(c.MaxSlots) > math.MaxInt32 ||
		int64(c.UIDBase)+2*int64(c.MaxSlots)+1 > math.MaxInt32 {
		return Identity{}, errors.New("isolation: UID 范围超出 Linux uid 上限,请调低 UIDBase 或 MaxSlots")
	}
	if slot < 1 || slot > c.AllocatableSlots() {
		return Identity{}, fmt.Errorf("isolation: 槽位 %d 越界(可分配 1..%d,其中 %d 与 %d 保留给自检)",
			slot, c.AllocatableSlots(), c.MaxSlots-1, c.MaxSlots)
	}
	return identityForSlot(c, slot), nil
}

// identityForSlot 按公式直接推出身份,不做槽位校验(自检的保留槽位也走这里):
// PiUID = UIDBase+2k,ExecUID = PiUID+1,GID = PiUID。
func identityForSlot(c Config, slot int) Identity {
	pi := uint32(c.UIDBase + 2*slot)
	return Identity{Slot: slot, PiUID: pi, ExecUID: pi + 1, GID: pi}
}

// AllocatableSlots 返回可分配给会话的槽位数(MaxSlots-2)。
func (c Config) AllocatableSlots() int {
	c = c.Normalize()
	return c.MaxSlots - 2
}

// SelectSlot 返回 used 之外最小的空闲槽位(1..MaxSlots-2);全满返回 ErrNoSlot。
func SelectSlot(c Config, used map[int]bool) (int, error) {
	c = c.Normalize()
	if c.AllocatableSlots() < 1 {
		return 0, ErrNoSlot
	}
	if _, err := IdentityFor(c, 1); err != nil {
		return 0, err // uid 超出内核上限等配置问题
	}
	for slot := 1; slot <= c.AllocatableSlots(); slot++ {
		if !used[slot] {
			return slot, nil
		}
	}
	return 0, ErrNoSlot
}

// PiCredential 返回拉起 Pi 进程用的凭据(exec.Cmd 的 SysProcAttr.Credential)。
func (id Identity) PiCredential() *syscall.Credential {
	return &syscall.Credential{Uid: id.PiUID, Gid: id.GID, Groups: []uint32{id.GID}}
}

// ExecCredential 返回以 ExecUID 执行命令用的凭据;与 Pi 同组但不同 uid。
func (id Identity) ExecCredential() *syscall.Credential {
	return &syscall.Credential{Uid: id.ExecUID, Gid: id.GID, Groups: []uint32{id.GID}}
}
