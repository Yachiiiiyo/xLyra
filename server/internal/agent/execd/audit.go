package execd

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

const maxAuditCommandBytes = 4 << 10

// AuditEvent 是一条命令的审计记录;不包含环境变量与输出内容。
type AuditEvent struct {
	Time        time.Time     `json:"time"`
	RunID       string        `json:"run_id"`
	Session     string        `json:"session"`
	Command     string        `json:"command"`
	Cwd         string        `json:"cwd"`
	ExitCode    int           `json:"exit_code"`
	Signal      string        `json:"signal"`
	TimedOut    bool          `json:"timed_out"`
	Cancelled   bool          `json:"cancelled"`
	Duration    time.Duration `json:"duration"`
	OutputBytes int64         `json:"output_bytes"`
	Reaped      int           `json:"reaped"`
	Error       string        `json:"error"`
}

// AuditSink 接收每条命令结束时的记录(含校验拒绝)。
type AuditSink interface {
	Record(AuditEvent)
}

type fileAudit struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileAudit 创建 0600 的 JSONL 追加审计文件。后续写失败只记日志,
// 不影响命令执行;调用方进程以 root 运行时文件自然归 root。
func NewFileAudit(path string) (AuditSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileAudit{f: f}, nil
}

func (a *fileAudit) Record(event AuditEvent) {
	event.Command = truncateCommand(event.Command)
	line, err := json.Marshal(event)
	if err != nil {
		slog.Error("execd: 编码审计记录失败", "error", err)
		return
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(line); err != nil {
		slog.Error("execd: 写入审计记录失败", "error", err)
	}
}

func truncateCommand(cmd string) string {
	if len(cmd) <= maxAuditCommandBytes {
		return cmd
	}
	return cmd[:maxAuditCommandBytes] + "…[truncated]"
}
