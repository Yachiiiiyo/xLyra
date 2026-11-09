package runner

import (
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/sandbox"
)

// ForgetSession 清理会话身份下残留的 Pi 与命令进程。删除会话目录前调用;
// 没有槽位、元数据不可读或清理失败都按尽力而为处理,不阻塞删除。
func (r *Runner) ForgetSession(sessionID string) error {
	if r.cfg.Isolation == nil {
		return nil
	}
	meta, err := r.cfg.Store.Get(sessionID)
	if err != nil || meta.Slot == 0 {
		return nil
	}
	identity, err := isolation.IdentityFor(r.cfg.Isolation.Config, meta.Slot)
	if err != nil {
		r.logger.Warn("会话身份无效,跳过残留进程清理", "session_id", sessionID, "slot", meta.Slot, "error", err)
		return nil
	}
	for _, uid := range []uint32{identity.PiUID, identity.ExecUID} {
		if _, _, err := sandbox.KillUID(int(uid)); err != nil {
			r.logger.Warn("清理会话残留进程失败", "session_id", sessionID, "uid", uid, "error", err)
		}
	}
	return nil
}
