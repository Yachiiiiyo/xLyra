package sandbox

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// procEntry 是一次 /proc 扫描的原始结果:进程信息加四个 uid。
type procEntry struct {
	Proc
	uids [4]int
}

// scanProcs 扫一遍 /proc;进程随时消失属正常,读不到就跳过。
func scanProcs() []procEntry {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []procEntry
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		_, comm, state, ppid, err := parseProcStat(string(statBytes))
		if err != nil {
			continue
		}
		statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		uids, ok := parseStatusUids(string(statusBytes))
		if !ok {
			continue
		}
		out = append(out, procEntry{Proc: Proc{Pid: pid, PPid: ppid, State: state, Cmd: comm}, uids: uids})
	}
	return out
}

func entryMatchesUID(e procEntry, uid int) bool {
	for _, u := range e.uids {
		if u == uid {
			return true
		}
	}
	return false
}

func entryInRange(e procEntry, base, count int) bool {
	for _, u := range e.uids {
		if u >= base && u < base+count {
			return true
		}
	}
	return false
}

// ProcsOfUID 返回属于 uid 的进程(real/effective/saved/fs 任一匹配),
// 含僵尸(State == "Z"),由调用方按需过滤。
func ProcsOfUID(uid int) []Proc {
	var out []Proc
	for _, e := range scanProcs() {
		if entryMatchesUID(e, uid) {
			out = append(out, e.Proc)
		}
	}
	return out
}

// killLoop 是按 uid 清理的公共轮询骨架:每轮对存活(非僵尸)进程发
// SIGKILL,连续 3 轮无存活即停,最多约 2 秒。match 决定本轮要杀哪些条目
// (KillUID 单 uid;SweepUIDs 按 uid 区间)。绝不 wait4(-1)——那是 helper
// subreaper 小进程的专属行为,主进程调用会偷走其他子进程的收尸。
func killLoop(match func(procEntry) bool) (killed, remaining int) {
	self := os.Getpid()
	seen := map[int]bool{}
	stable := 0
	for round := 0; round < 100; round++ {
		alive := 0
		for _, e := range scanProcs() {
			if e.State == "Z" || e.Pid == self {
				continue
			}
			if !match(e) {
				continue
			}
			alive++
			if err := unix.Kill(e.Pid, unix.SIGKILL); err == nil || err == unix.ESRCH {
				seen[e.Pid] = true
			}
		}
		if alive == 0 {
			stable++
			if stable >= 3 {
				break
			}
		} else {
			stable = 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, e := range scanProcs() {
		if e.State != "Z" && e.Pid != self && match(e) {
			remaining++
		}
	}
	return len(seen), remaining
}

// KillUID 是主进程版清理:只发 SIGKILL、不 wait4,僵尸不算存活(其收尸归
// 属父进程/init)。不杀 uid 0 与当前进程 euid——传 0 或自身 uid 返回错误
// 且不动手。
func KillUID(uid int) (killed, remaining int, err error) {
	if uid <= 0 {
		return 0, 0, fmt.Errorf("sandbox: 拒绝清理 uid %d", uid)
	}
	if uid == os.Geteuid() {
		return 0, 0, fmt.Errorf("sandbox: 拒绝清理自身 uid %d", uid)
	}
	killed, remaining = killLoop(func(e procEntry) bool { return entryMatchesUID(e, uid) })
	return killed, remaining, nil
}

// SweepUIDs 对 [base, base+count) 内每个 uid 做启动清理(崩溃后的残留):
// 每轮只扫一次 /proc、按 uid 区间分发,不逐 uid 重扫。返回杀掉的进程数
// (尽力而为,不报错)。
func SweepUIDs(base, count int) int {
	if base <= 0 || count <= 0 {
		return 0
	}
	killed, _ := killLoop(func(e procEntry) bool { return entryInRange(e, base, count) })
	return killed
}
