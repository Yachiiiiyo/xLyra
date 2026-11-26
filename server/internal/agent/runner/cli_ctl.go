package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/agentctl"
	"xlyra/server/internal/auth"
)

const defaultCLIRunWriteQuota = 60

func mergeSkillDirs(snapshotDirs, builtinDirs []string) []string {
	seen := make(map[string]struct{}, len(snapshotDirs)+len(builtinDirs))
	out := make([]string, 0, len(snapshotDirs)+len(builtinDirs))
	appendValid := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, err := os.Stat(dir); err != nil {
			return
		}
		if _, dup := seen[dir]; dup {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	for _, dir := range snapshotDirs {
		appendValid(dir)
	}
	for _, dir := range builtinDirs {
		appendValid(dir)
	}
	return out
}

func (r *Runner) startRunControlServer(ar *activeRun, prep *prepared, mode string, identity *isolation.Identity) (*agentctl.UDSServer, error) {
	if r.cfg.CtlEngine == nil {
		return nil, nil
	}
	compactID := strings.ReplaceAll(ar.runID, "-", "")
	if len(compactID) > 12 {
		compactID = compactID[:12]
	}
	if compactID == "" {
		compactID = uuid.NewString()[:12]
	}
	// Unix socket 路径保持短路径，避免触及 macOS 104 字节 sun_path 上限；
	// 在隔离模式下目录给 0711、socket 给 0600 并移交给 ExecUID，使 PiUID 无法连接。
	sockDir := filepath.Join(os.TempDir(), "xctl-"+compactID)
	if len(filepath.Join(sockDir, "ctl.sock")) >= 98 {
		sockDir = "/tmp/xctl-" + compactID
	}
	if err := os.MkdirAll(sockDir, 0o711); err != nil {
		return nil, fmt.Errorf("runner: 创建控制面目录: %w", err)
	}
	sockPath := filepath.Join(sockDir, "ctl.sock")

	permMode := permissionModeValue(mode)
	udsCfg := agentctl.UDSConfig{
		SocketPath: sockPath,
		SocketMode: 0o600,
		Caller: agentctl.Caller{
			Channel:        agentctl.ChannelAgentCLI,
			RunID:          ar.runID,
			SessionID:      ar.sessionID,
			ActorType:      auth.AgentCLIActorType,
			PermissionMode: permMode,
			Policy:         agentctl.DefaultBuiltinAgentPolicy(),
			Quota:          agentctl.NewRunQuota(defaultCLIRunWriteQuota),
			Approver:       r.newCLIApprover(ar, prep, permMode),
		},
		Logger: r.logger,
	}
	if identity != nil {
		uid := int(identity.ExecUID)
		gid := int(identity.GID)
		execUID := uint32(identity.ExecUID)
		udsCfg.SocketOwnerUID = &uid
		udsCfg.SocketOwnerGID = &gid
		if r.cfg.Sandbox != nil {
			udsCfg.ExpectedUID = &execUID
		}
	}

	srv, err := agentctl.StartUDSServer(r.cfg.CtlEngine, udsCfg)
	if err != nil {
		_ = os.RemoveAll(sockDir)
		return nil, fmt.Errorf("runner: 启动控制面 socket: %w", err)
	}
	return srv, nil
}

func closeRunControlServer(srv *agentctl.UDSServer) {
	if srv == nil {
		return
	}
	sockDir := filepath.Dir(srv.SocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Close(ctx)
	if sockDir != "" && strings.HasPrefix(filepath.Base(sockDir), "xctl-") {
		_ = os.RemoveAll(sockDir)
	}
}

func (r *Runner) newCLIApprover(ar *activeRun, prep *prepared, permMode string) agentctl.Approver {
	return agentctl.ApproverFunc(func(ctx context.Context, req agentctl.ApprovalRequest) (agentctl.ApprovalDecision, error) {
		if permMode == "full" {
			return agentctl.ApprovalDecision{Granted: true}, nil
		}

		cmdLine := formatCLICommand(req)
		diffNote := formatCLIDiffNote(req)
		escalationID := "ctl-" + uuid.NewString()

		// auto 模式下仅 Tier 2 走 AI 分类器；Tier 3 或缺少分类器则进入单次人工确认卡片。
		if permMode == "auto" && req.Tier <= agentctl.Tier2Managed && r.cfg.Classify != nil {
			return r.classifyAndPublishCLIDecision(ctx, ar, escalationID, cmdLine, diffNote, req)
		}

		// 人工审批弹窗期间暂停命令超时计时，避免长时审阅触发 120s/600s 超时中止。
		if prep != nil && prep.executor != nil {
			prep.executor.PauseTimer()
			defer prep.executor.ResumeTimer()
		}

		ch := make(chan bool, 1)
		ar.stateMu.Lock()
		if ar.cliApprovals == nil {
			ar.cliApprovals = map[string]chan bool{}
		}
		ar.cliApprovals[escalationID] = ch
		ar.stateMu.Unlock()

		risk := ""
		if req.Tier >= agentctl.Tier3HighRisk {
			risk = "high"
		}
		escPayload := map[string]any{
			"escalation_id":     escalationID,
			"tool_name":         "xlyra",
			"requested_command": []string{cmdLine},
			"note":              diffNote,
		}
		if risk != "" {
			escPayload["risk"] = risk
			escPayload["risk_rules"] = []string{fmt.Sprintf("Tier %d 控制面变更", req.Tier)}
		}
		if len(req.Diff) > 0 {
			escPayload["diff"] = req.Diff
		}
		event := translate.Event{
			Type: "escalation_request",
			Data: map[string]any{"escalation": escPayload},
		}

		r.publish(ar, event)
		r.recordInteractiveCLIEscalation(ar, escalationID, cmdLine, diffNote, risk)

		select {
		case granted, ok := <-ch:
			if !ok || !granted {
				return agentctl.ApprovalDecision{
					Granted: false,
					Code:    agentctl.ErrCodeUserDenied,
					Reason:  "用户拒绝了本次控制面变更",
				}, nil
			}
			return agentctl.ApprovalDecision{Granted: true}, nil
		case <-ctx.Done():
			r.resolveEscalation(ar, escalationID, resolveOptions{granted: false, reason: "请求超时或已断开"})
			return agentctl.ApprovalDecision{
				Granted: false,
				Code:    agentctl.ErrCodeApprovalTimeout,
				Reason:  "审批等待超时或连接中断",
			}, nil
		case <-ar.ctx.Done():
			r.resolveEscalation(ar, escalationID, resolveOptions{granted: false, reason: "Agent 运行已中止"})
			return agentctl.ApprovalDecision{
				Granted: false,
				Code:    agentctl.ErrCodeApprovalTimeout,
				Reason:  "Agent 运行已中止",
			}, nil
		}
	})
}

func (r *Runner) classifyAndPublishCLIDecision(ctx context.Context, ar *activeRun, id, cmdLine, diffNote string, req agentctl.ApprovalRequest) (agentctl.ApprovalDecision, error) {
	started := time.Now()
	granted, reason := r.classifyCLIAction(ctx, ar, cmdLine, req)

	escPayload := map[string]any{
		"escalation_id":     id,
		"tool_name":         "xlyra",
		"requested_command": []string{cmdLine},
		"note":              diffNote,
	}
	if len(req.Diff) > 0 {
		escPayload["diff"] = req.Diff
	}
	reqEvent := translate.Event{
		Type: "escalation_request",
		Data: map[string]any{"escalation": escPayload},
	}

	ar.stateMu.Lock()
	afterEntry := ar.lastEntryID
	ar.stateMu.Unlock()
	if afterEntry == "" {
		afterEntry = r.anchorFromSessionFile(ar)
	}

	value := granted
	record := session.Escalation{
		EscalationID:     id,
		ToolName:         "xlyra",
		RequestedCommand: []string{cmdLine},
		Note:             diffNote,
		AfterEntryID:     afterEntry,
		At:               time.Now().UTC(),
		Granted:          &value,
		Auto:             true,
		ClassifierReason: reason,
	}
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		meta.Escalations = append(meta.Escalations, record)
		return nil
	}); err != nil {
		r.logger.Error("记录控制面自动审批失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}

	r.publish(ar, reqEvent)
	r.publish(ar, ar.tr.AutoDecision(id, granted, reason))
	r.logger.Debug("控制面审批分类结束", "session_id", ar.sessionID, "run_id", ar.runID,
		"escalation_id", id, "granted", granted, "duration_ms", time.Since(started).Milliseconds(), "reason", reason)

	if granted {
		ar.autoDenyStreak.Store(0)
		return agentctl.ApprovalDecision{Granted: true, Reason: reason}, nil
	}
	return agentctl.ApprovalDecision{
		Granted: false,
		Code:    agentctl.ErrCodeClassifierDenied,
		Reason:  reason,
	}, nil
}

func (r *Runner) recordInteractiveCLIEscalation(ar *activeRun, id, cmdLine, diffNote, risk string) {
	ar.stateMu.Lock()
	afterEntry := ar.lastEntryID
	esc := session.Escalation{
		EscalationID:     id,
		ToolName:         "xlyra",
		RequestedCommand: []string{cmdLine},
		Note:             diffNote,
		Risk:             risk,
		AfterEntryID:     afterEntry,
		At:               time.Now().UTC(),
		Reviewing:        false,
	}
	ar.pending[id] = esc
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()

	if esc.AfterEntryID == "" {
		esc.AfterEntryID = r.anchorFromSessionFile(ar)
	}
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		record := esc
		ar.stateMu.Lock()
		opts, decided := ar.resolved[id]
		ar.stateMu.Unlock()
		if decided {
			val := opts.granted
			record.Granted = &val
			record.ClassifierReason = opts.reason
		}
		meta.Escalations = append(meta.Escalations, record)
		return nil
	}); err != nil {
		r.logger.Error("记录控制面审批失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
}

func (r *Runner) cancelPendingCLIApprovals(ar *activeRun) {
	ar.stateMu.Lock()
	chans := make([]chan bool, 0, len(ar.cliApprovals))
	for id, ch := range ar.cliApprovals {
		delete(ar.cliApprovals, id)
		chans = append(chans, ch)
	}
	ar.stateMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- false:
		default:
		}
		close(ch)
	}
}

func (r *Runner) deliverCLIApproval(ar *activeRun, id string, granted bool) bool {
	ar.stateMu.Lock()
	ch, ok := ar.cliApprovals[id]
	if ok {
		delete(ar.cliApprovals, id)
	}
	ar.stateMu.Unlock()
	if !ok || ch == nil {
		return false
	}
	select {
	case ch <- granted:
	default:
	}
	close(ch)
	return true
}

func formatCLICommand(req agentctl.ApprovalRequest) string {
	parts := []string{"xlyra", req.Resource, req.Action}
	if strings.TrimSpace(req.Target) != "" {
		parts = append(parts, strings.TrimSpace(req.Target))
	}
	if strings.TrimSpace(req.SubTarget) != "" {
		parts = append(parts, strings.TrimSpace(req.SubTarget))
	}
	return strings.Join(parts, " ")
}

func formatCLIDiffNote(req agentctl.ApprovalRequest) string {
	var b strings.Builder
	if strings.TrimSpace(req.Summary) != "" {
		b.WriteString(strings.TrimSpace(req.Summary))
	} else {
		fmt.Fprintf(&b, "控制面操作 %s %s (Tier %d)", req.Resource, req.Action, req.Tier)
	}
	if len(req.Diff) > 0 {
		b.WriteString("\n变更明细:")
		limit := min(len(req.Diff), 8)
		for i := 0; i < limit; i++ {
			d := req.Diff[i]
			fmt.Fprintf(&b, "\n- %s: %s -> %s", d.Path, formatCompactJSON(d.Before), formatCompactJSON(d.After))
		}
		if len(req.Diff) > limit {
			fmt.Fprintf(&b, "\n- ... 另有 %d 项变更", len(req.Diff)-limit)
		}
	}
	return b.String()
}

func formatCompactJSON(v any) string {
	if v == nil {
		return "null"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(raw)
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func prependPathDir(dir, pathEnv string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return pathEnv
	}
	parts := strings.Split(pathEnv, ":")
	if slices.Contains(parts, dir) {
		return pathEnv
	}
	if strings.TrimSpace(pathEnv) == "" {
		return dir
	}
	return dir + ":" + pathEnv
}
