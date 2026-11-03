package sandbox

import (
	"errors"
	"strconv"
	"strings"
)

// Proc 是 /proc 里属于某个 uid 的进程条目(扫描逻辑在 proc_linux.go)。
type Proc struct {
	Pid   int
	PPid  int
	State string // /proc/<pid>/stat 的 state 字段:R/S/D/Z/T…
	Cmd   string // comm,内核截断到 15 字符,足够清理与诊断用
}

// parseProcStat 解析 /proc/<pid>/stat 内容。comm 可以包含空格与括号,必须
// 按第一个 '(' 与最后一个 ')' 切分。字段序:pid (comm) state ppid …。
func parseProcStat(content string) (pid int, comm, state string, ppid int, err error) {
	open := strings.IndexByte(content, '(')
	close := strings.LastIndexByte(content, ')')
	if open <= 0 || close <= open {
		return 0, "", "", 0, errors.New("sandbox: /proc stat 缺少 (comm)")
	}
	pid, err = strconv.Atoi(strings.TrimSpace(content[:open]))
	if err != nil {
		return 0, "", "", 0, err
	}
	comm = content[open+1 : close]
	rest := strings.Fields(content[close+1:])
	if len(rest) < 2 {
		return 0, "", "", 0, errors.New("sandbox: /proc stat 字段不足")
	}
	ppid, err = strconv.Atoi(rest[1])
	if err != nil {
		return 0, "", "", 0, err
	}
	return pid, comm, rest[0], ppid, nil
}

// parseStatusUids 解析 /proc/<pid>/status 的 Uid: 行(real/effective/saved/fs)。
// 清理进程时四个 uid 任一匹配即视为属于目标身份(进程可能在 setuid 过程中)。
func parseStatusUids(content string) ([4]int, bool) {
	var uids [4]int
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return uids, false
		}
		for i := range uids {
			v, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return uids, false
			}
			uids[i] = v
		}
		return uids, true
	}
	return uids, false
}
