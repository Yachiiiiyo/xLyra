package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/execd"
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/piconfig"
	"xlyra/server/internal/agent/pirpc"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

func (r *Runner) prepare(ar *activeRun, spec runSpec, piSessionID string) (*prepared, error) {
	// 能力快照决定技能、AGENTS.md 与上下文参数;读取失败不阻断对话。
	snap := r.snapshot(ar)
	// 隔离:分配(或复用)会话身份并修正目录布局;任何失败都让本 run
	// 以 agent_error 收尾,不降级为无隔离启动。
	var identity *isolation.Identity
	if r.cfg.Isolation != nil {
		var err error
		identity, err = r.isolate(ar)
		if err != nil {
			return nil, err
		}
	}
	protocol, err := r.cfg.Protocol(ar.ctx, spec.model)
	if err != nil {
		return nil, fmt.Errorf("runner: 解析模型协议: %w", err)
	}
	caps := piconfig.CapsFor(spec.model)
	agentDir := filepath.Join(r.cfg.Store.Dir(ar.sessionID), "pi-agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return nil, fmt.Errorf("runner: 创建 Pi 配置目录: %w", err)
	}
	// 令牌先签发再落盘:models.json 的 apiKey 用 "!cat <文件>" 命令引用,
	// Pi 每次请求都重新执行,后续轮换只需原子重写该文件。
	level := ""
	if spec.effort != "" {
		level = piconfig.ThinkingLevel(spec.effort, caps)
	}
	prep := &prepared{level: level, tokenFile: filepath.Join(agentDir, "token"), identity: identity}
	var bashConfig *xlyraBashConfig
	if r.cfg.Sandbox != nil && r.cfg.Sandbox.Bash && ar.kind != "compact" {
		sessionDir := r.cfg.Store.Dir(ar.sessionID)
		audit, auditErr := execd.NewFileAudit(filepath.Join(sessionDir, "exec-audit.jsonl"))
		if auditErr != nil {
			r.logger.Warn("创建命令审计文件失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", auditErr)
		}
		prep.executor, err = execd.New(execd.Config{
			Exe: r.cfg.Sandbox.Exe, SessionDir: sessionDir,
			PiUID: int(identity.PiUID), ExecUID: int(identity.ExecUID), GID: int(identity.GID),
			Workspace:      r.cfg.Store.WorkDir(ar.sessionID),
			DefaultTimeout: r.cfg.Sandbox.ExecDefaultTimeout, MaxTimeout: r.cfg.Sandbox.ExecMaxTimeout,
			Audit: audit, Logger: r.logger,
		})
		if err != nil {
			return prep, fmt.Errorf("runner: 创建命令执行器: %w", err)
		}
		sock, err := prep.executor.Start()
		if err != nil {
			return prep, fmt.Errorf("runner: 启动命令执行器: %w", err)
		}
		prep.executor.SetRun(ar.runID)
		bashConfig = &xlyraBashConfig{
			Enabled: true, Socket: sock,
			DefaultTimeout: int(r.cfg.Sandbox.ExecDefaultTimeout / time.Second),
			MaxTimeout:     int(r.cfg.Sandbox.ExecMaxTimeout / time.Second),
		}
	}
	token, _, err := r.cfg.Tokens.IssueRunToken(ar.ctx, ar.sessionID, ar.runID, spec.model)
	if err != nil {
		r.logger.Error("签发 Agent 令牌失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		return prep, fmt.Errorf("runner: 签发 run 令牌: %w", err)
	}
	prep.tokenIssued = true
	prep.addToken(token)
	if err := writeTokenFile(prep.tokenFile, token, identity); err != nil {
		return prep, fmt.Errorf("runner: 写入令牌文件: %w", err)
	}
	modelsJSON, err := piconfig.ModelsJSON(piconfig.ModelsInput{
		ModelID: spec.model, Protocol: protocol, BaseURL: r.cfg.GatewayBaseURL, RunID: ar.runID,
		TokenFile: prep.tokenFile, ContextWindow: snap.ContextWindow,
	})
	if err != nil {
		return prep, fmt.Errorf("runner: 生成 models.json: %w", err)
	}
	settingsJSON, err := piconfig.SettingsJSON(piconfig.SettingsInput{
		ModelID: spec.model, ThinkingLevel: prep.level,
		CompactionReserveTokens: compactionReserveTokens(snap.ContextWindow, snap.CompactTriggerRatio),
	})
	if err != nil {
		return prep, fmt.Errorf("runner: 生成 settings.json: %w", err)
	}
	// 每个 run 重建配置,避免模型、run 请求头或上一个 run 的权限状态残留;
	// 系统提示与 xlyra.json 同样逐 run 重写(权限模式非 full 一律 ask)。
	imageGeneration := r.imageGenerationConfig(ar.kind, prep.tokenFile)
	imageEnabled := imageGeneration != nil
	// question 对话类 run 都开放(含编辑重发的 retry),与 bash/生图一致;只有 compact 不开放。
	questionEnabled := ar.kind != "compact"
	execDefaultTimeout := time.Duration(0)
	if r.cfg.Sandbox != nil {
		execDefaultTimeout = r.cfg.Sandbox.ExecDefaultTimeout
	}
	files := map[string][]byte{
		"models.json":      modelsJSON,
		"settings.json":    settingsJSON,
		"system-prompt.md": []byte(r.systemPrompt(prep.executor != nil, execDefaultTimeout, imageEnabled, questionEnabled)),
		"xlyra.json":       xlyraConfigJSON(spec.mode, snap.SkillDirs, bashConfig, imageGeneration),
	}
	for name, data := range files {
		path := filepath.Join(agentDir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return prep, fmt.Errorf("runner: 写入 %s: %w", name, err)
		}
		// WriteFile 不改已有文件的模式,重写时显式回收权限;隔离模式下
		// 移交给会话身份(ChownFile 同时收紧到 0600)。
		if identity != nil {
			if err := isolation.ChownFile(path, *identity); err != nil {
				return prep, fmt.Errorf("runner: 移交 %s: %w", name, err)
			}
		} else if err := os.Chmod(path, 0o600); err != nil {
			return prep, fmt.Errorf("runner: 收紧 %s 权限: %w", name, err)
		}
	}
	// AGENTS.md 只认 PI_CODING_AGENT_DIR 下这一份;为空时删除,防止上一次 run
	// 的残留继续生效。隔离模式下文件属会话身份,root 仍可删除。
	agentsPath := filepath.Join(agentDir, "AGENTS.md")
	if snap.AgentsMD != "" {
		if err := writeAtomicFile(agentsPath, []byte(snap.AgentsMD), identity); err != nil {
			return prep, fmt.Errorf("runner: 写入 AGENTS.md: %w", err)
		}
	} else if err := os.Remove(agentsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return prep, fmt.Errorf("runner: 清理 AGENTS.md: %w", err)
	}

	piDir := r.cfg.Store.PiDir(ar.sessionID)
	args := append([]string(nil), r.cfg.NodeFlags...)
	args = append(args, r.cfg.Args...)
	args = append(args, r.cfg.ExtraArgs...)
	args = append(args, "--mode", "rpc", "--session-dir", piDir)
	if piSessionID != "" {
		args = append(args, "--session-id", piSessionID)
	}
	args = append(args, "--provider", piconfig.ProviderName, "--model", spec.model)
	if extension := strings.TrimSpace(r.cfg.ExtensionPath); extension != "" {
		args = append(args, "-e", extension)
	}
	args = append(args, "--system-prompt", filepath.Join(agentDir, "system-prompt.md"))
	if tools := r.runTools(prep.executor != nil, imageEnabled, questionEnabled); tools != "" {
		args = append(args, "--tools", tools)
	}
	// 技能发现固定关闭,只加载快照里启用的目录(停用 = 不传,不依赖
	// settings 排除写法);AGENTS.md 发现被关闭时加 --no-context-files。
	args = append(args, "--no-skills")
	for _, dir := range snap.SkillDirs {
		args = append(args, "--skill", dir)
	}
	if !snap.EnableAgentsMD {
		args = append(args, "--no-context-files")
	}
	// 隔离模式下会话目录归 root,Pi 不能往里写:HOME 改指向 Pi 自己的
	// pi-agent/;关闭隔离时保持原来的会话目录。
	home := r.cfg.Store.Dir(ar.sessionID)
	if identity != nil {
		home = agentDir
	}
	env := piconfig.Env(piconfig.EnvInput{
		Home: home, AgentDir: agentDir, SessionDir: piDir,
		Path: r.cfg.PathEnv,
	})
	// 进程生命周期不由停止信号直接取消:Stop 要先 Abort 等待 agent_settled,
	// 提前 Kill 会把取消变成 agent_error;收尾统一走 cleanup 的 Close。
	piConfig := pirpc.Config{
		Command: r.cfg.Command, Args: args, Dir: r.cfg.Store.WorkDir(ar.sessionID),
		Env: env, Logger: r.logger, Credential: credential(identity),
	}
	if r.cfg.Sandbox != nil && r.cfg.Sandbox.PiLandlock && identity != nil {
		port, err := gatewayConnectPort(r.cfg.GatewayBaseURL)
		if err != nil {
			return prep, err
		}
		piEntry := ""
		if len(r.cfg.Args) > 0 {
			piEntry = r.cfg.Args[0]
		}
		readWrite := []string{piDir, agentDir}
		if prep.executor != nil {
			readWrite = append(readWrite, filepath.Join(r.cfg.Store.Dir(ar.sessionID), "exec"))
		}
		spec := sandbox.Spec{
			UID: int(identity.PiUID), GID: int(identity.GID),
			Workspace: r.cfg.Store.WorkDir(ar.sessionID), Cwd: r.cfg.Store.WorkDir(ar.sessionID),
			ReadOnly:  piReadOnlyPaths(r.cfg.Command, piEntry, strings.TrimSpace(r.cfg.ExtensionPath), snap.SkillDirs, r.cfg.PathEnv),
			ReadWrite: readWrite, Net: sandbox.NetConnectPorts, ConnectPorts: []int{port},
			Umask: 0o002, Env: env, Limits: sandbox.Limits{NoFile: 4096, Core: 0},
		}
		argv := append([]string{r.cfg.Command}, args...)
		cmd, err := sandbox.ChildCommand(r.cfg.Sandbox.Exe, spec, argv)
		if err != nil {
			return prep, fmt.Errorf("runner: 构造 Pi 沙箱: %w", err)
		}
		piConfig.Command, piConfig.Args, piConfig.Env = cmd.Path, cmd.Args[1:], cmd.Env
		piConfig.Credential = cmd.SysProcAttr.Credential
	}
	proc, err := pirpc.Start(context.Background(), piConfig)
	if err != nil {
		return prep, fmt.Errorf("runner: 启动 Pi: %w", err)
	}
	prep.proc = proc
	ar.proc.Store(proc)
	// run 存活期间持续续期令牌;cleanup 会 cancel ar.ctx 并等本 goroutine 退出。
	ar.renewWG.Add(1)
	go r.renewLoop(ar, prep)
	if ar.kind != "compact" {
		ar.renewWG.Add(1)
		go r.quotaLoop(ar, prep)
	}
	r.logger.Info("Agent run 开始", "session_id", ar.sessionID, "run_id", ar.runID, "kind", ar.kind, "model", spec.model)
	return prep, nil
}

// isolate 分配(或复用)会话的隔离身份并修正目录布局,每次 run 调用均
// 幂等:槽位写进 meta,同一会话后续 run 复用同一身份。任一步失败都返回
// 错误使该 run 以 agent_error 收尾,绝不降级为无隔离启动。
func (r *Runner) isolate(ar *activeRun) (*isolation.Identity, error) {
	pool := r.cfg.Isolation.Config
	slot, err := r.cfg.Store.AssignSlot(ar.sessionID, func(used map[int]bool) (int, error) {
		return isolation.SelectSlot(pool, used)
	})
	if err != nil {
		return nil, fmt.Errorf("无法分配隔离身份: %w", err)
	}
	identity, err := isolation.IdentityFor(pool, slot)
	if err != nil {
		return nil, fmt.Errorf("无法分配隔离身份: %w", err)
	}
	if err := r.cfg.Store.EnsureDirs(ar.sessionID); err != nil {
		return nil, fmt.Errorf("准备会话目录失败: %w", err)
	}
	if err := isolation.PrepareSession(r.cfg.Store.Dir(ar.sessionID), identity); err != nil {
		return nil, fmt.Errorf("准备隔离会话目录失败: %w", err)
	}
	return &identity, nil
}

// credential 返回启动 Pi 用的凭据;隔离关闭时为 nil(与宿主同身份)。
func credential(identity *isolation.Identity) *syscall.Credential {
	if identity == nil {
		return nil
	}
	return identity.PiCredential()
}

// snapshot 取能力快照并归一化上下文参数;未装配或读取失败时退回默认
// (技能与 AGENTS.md 关闭、水位线 0.9、窗口 200000),只记 Warn 不阻断 run。
func (r *Runner) snapshot(ar *activeRun) capabilities.Snapshot {
	var snap capabilities.Snapshot
	if r.cfg.Capabilities != nil {
		var err error
		snap, err = r.cfg.Capabilities.Snapshot()
		if err != nil {
			r.logger.Warn("读取 Agent 能力快照失败,使用默认设置", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
	}
	if snap.CompactTriggerRatio <= 0 {
		snap.CompactTriggerRatio = 0.9
	}
	if snap.ContextWindow <= 0 {
		snap.ContextWindow = 200000
	}
	return snap
}

// compactionReserveTokens 把水位线换算成 Pi 的 compaction.reserveTokens:
// 上下文 token 超过 窗口-reserve 时触发自动压缩(compaction.md),下限
// 1024 防止小窗口高水位时留给回复的空间过小。
func compactionReserveTokens(window int, ratio float64) int {
	reserve := int(math.Round(float64(window) * (1 - ratio)))
	if reserve < 1024 {
		reserve = 1024
	}
	return reserve
}

// renewLoop 周期性续期令牌并原子重写令牌文件;每次签发都会 supersede 旧令牌
// (旧令牌只剩 SupersededGracePeriod 的宽限),Pi 每请求重读文件,轮换即生效。
// 失败只记 Warn(不含令牌值),下个间隔重试;ar.ctx 结束时退出。
func (r *Runner) renewLoop(ar *activeRun, prep *prepared) {
	defer ar.renewWG.Done()
	ticker := time.NewTicker(r.cfg.TokenRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ar.ctx.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(ar.ctx, 30*time.Second)
		token, _, err := r.cfg.Tokens.IssueRunToken(ctx, ar.sessionID, ar.runID, ar.model)
		cancel()
		if err != nil {
			// 收尾触发的取消不算故障。
			if ar.ctx.Err() == nil {
				r.logger.Warn("续期 Agent 令牌失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			}
			continue
		}
		if err := writeTokenFile(prep.tokenFile, token, prep.identity); err != nil {
			r.logger.Warn("重写令牌文件失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			continue
		}
		prep.addToken(token)
	}
}

var quotaCheckInterval = 30 * time.Second

func (r *Runner) sessionQuotaExceeded(sessionID string) (bool, error) {
	size, err := dirSize(r.cfg.SessionQuotaBytes, r.cfg.Store.WorkDir(sessionID), r.cfg.Store.PiDir(sessionID),
		filepath.Join(r.cfg.Store.Dir(sessionID), "exec-tmp"), filepath.Join(r.cfg.Store.Dir(sessionID), "pi-agent"))
	return size > r.cfg.SessionQuotaBytes, err
}

// dirSize 累计多个目录里的普通文件大小;符号链接不跟随,不可读项跳过。
// 超过 limit 时立即返回当前总量,避免为确认超配额遍历完整棵目录树。
func dirSize(limit int64, paths ...string) (int64, error) {
	var total int64
	for _, root := range paths {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry == nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			total += info.Size()
			if limit >= 0 && total > limit {
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		if limit >= 0 && total > limit {
			return total, nil
		}
	}
	return total, nil
}

func (r *Runner) quotaLoop(ar *activeRun, prep *prepared) {
	defer ar.renewWG.Done()
	ticker := time.NewTicker(quotaCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ar.ctx.Done():
			return
		case <-ticker.C:
		}
		exceeded, err := r.sessionQuotaExceeded(ar.sessionID)
		if err != nil {
			r.logger.Warn("检查会话磁盘配额失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			continue
		}
		if !exceeded {
			continue
		}
		if r.publishFinal(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "工作区超过磁盘配额,已中止"}}, "disk_quota") {
			r.abort(ar, prep)
		}
		return
	}
}

// writeTokenFile 以 0600 原子写令牌文件;内容就是令牌本身(Pi 对命令输出
// 会 trim)。隔离模式下移交会话身份。
func writeTokenFile(path, token string, identity *isolation.Identity) error {
	return writeAtomicFile(path, []byte(token), identity)
}

// writeAtomicFile 以 0600 原子写文件:同目录临时文件 + rename,避免 Pi 在
// 写入中途读到半截内容。隔离模式下临时文件在 rename 之前就移交给会话身份
// (ChownFile 同时收紧到 0600),避免最终路径出现 root 属主的窗口期。
func writeAtomicFile(path string, data []byte, identity *isolation.Identity) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if identity != nil {
		if err := isolation.ChownFile(tmp, *identity); err != nil {
			return err
		}
	} else if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// xlyraConfigJSON 生成 xLyra 扩展读的运行配置:权限模式与只读根(启用技能的
// 目录,供扩展放宽 read/ls/grep/find;write/edit 仍限工作目录)。readRoots 恒为
// 数组,无技能时为空数组而不是 null,方便扩展侧统一校验。
type xlyraBashConfig struct {
	Enabled        bool   `json:"enabled"`
	Socket         string `json:"socket"`
	DefaultTimeout int    `json:"defaultTimeout"`
	MaxTimeout     int    `json:"maxTimeout"`
}

type xlyraImageGenerationConfig struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint"`
	TokenFile      string `json:"tokenFile"`
	MaxBytes       int    `json:"maxBytes"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

func (r *Runner) imageGenerationConfig(kind, tokenFile string) *xlyraImageGenerationConfig {
	if kind == "compact" || r.cfg.ImageModels == nil || r.cfg.ImageModels() == 0 {
		return nil
	}
	return &xlyraImageGenerationConfig{
		Enabled: true, Endpoint: strings.TrimRight(r.cfg.GatewayBaseURL, "/") + "/internal/agent-llm/images",
		TokenFile: tokenFile, MaxBytes: 32 << 20, TimeoutSeconds: 660,
	}
}

func xlyraConfigJSON(mode string, readRoots []string, bash *xlyraBashConfig, imageGeneration *xlyraImageGenerationConfig) []byte {
	if readRoots == nil {
		readRoots = []string{}
	}
	data, err := json.Marshal(struct {
		PermissionMode  string                      `json:"permissionMode"`
		ReadRoots       []string                    `json:"readRoots"`
		Bash            *xlyraBashConfig            `json:"bash,omitempty"`
		ImageGeneration *xlyraImageGenerationConfig `json:"imageGeneration,omitempty"`
	}{PermissionMode: permissionModeValue(mode), ReadRoots: readRoots, Bash: bash, ImageGeneration: imageGeneration})
	if err != nil {
		return []byte(`{"permissionMode":"ask","readRoots":[]}`)
	}
	return data
}

func (r *Runner) drive(ar *activeRun, spec runSpec, prep *prepared) {
	// 先确保事件循环就位再发命令:prompt 应答只表示受理,事件不能先于订阅。
	commandReady := make(chan struct{})
	commandErr := make(chan error, 1)
	go func() {
		<-commandReady
		if prep.level != "" {
			resp, err := prep.proc.Call(ar.ctx, map[string]any{"type": "set_thinking_level", "level": prep.level})
			if err != nil {
				commandErr <- fmt.Errorf("set_thinking_level: %w", err)
				return
			}
			if _, err := commandData(resp); err != nil {
				commandErr <- fmt.Errorf("set_thinking_level: %w", err)
				return
			}
		}
		command := map[string]any{"type": "prompt", "message": spec.text}
		if len(spec.images) > 0 {
			command["images"] = spec.images
		}
		resp, err := prep.proc.Call(ar.ctx, command)
		if err != nil {
			commandErr <- fmt.Errorf("prompt: %w", err)
			return
		}
		if _, err := commandData(resp); err != nil {
			commandErr <- fmt.Errorf("prompt: %w", err)
			return
		}
		commandErr <- nil
	}()

	timer := time.NewTimer(r.cfg.RunTimeout)
	defer timer.Stop()
	// 等待审批/提问期间暂停总时限:记下剩余时间并停表,全部解决后按剩余时间重启。
	var paused bool
	var remaining time.Duration
	var timerStart = time.Now()
	close(commandReady)
	lastEntryID := ""
	var outputBytes int64
	for {
		select {
		case raw, ok := <-prep.proc.Events():
			if !ok {
				info := prep.proc.Wait()
				ar.stateMu.Lock()
				terminal := ar.terminal
				ar.stateMu.Unlock()
				if terminal == "" {
					if ar.stopping.Load() {
						r.publish(ar, translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}})
					} else {
						message := fmt.Sprintf("Pi 进程提前退出(code %d)", info.Code)
						if tail := sanitizeStderr(info.Stderr, prep, 500); tail != "" {
							message += ": " + tail
						}
						r.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": message}})
					}
				}
				r.settleMeta(ar, "")
				return
			}
			outputBytes += int64(len(raw))
			if outputBytes > r.cfg.MaxOutputBytes {
				r.publishFinal(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "输出超过上限,已中止"}}, "output_limit")
				r.abort(ar, prep)
				// agent_settled 仍需驱动收尾,否则 Abort 后会一直等进程退出。
				if eventType(raw) != "agent_settled" {
					continue
				}
			}
			if id := appendedEntryID(raw); id != "" {
				lastEntryID = id
				ar.noteEntryID(id)
			}
			events, err := ar.tr.Feed(raw)
			if err != nil {
				ar.stateMu.Lock()
				terminal := ar.terminal
				ar.stateMu.Unlock()
				if terminal == "" {
					if ar.stopping.Load() {
						r.publish(ar, translate.Event{Type: "agent_cancelled", Data: map[string]any{"aborted": true}})
					} else {
						r.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "Pi 事件解析失败: " + err.Error()}})
					}
				}
				r.abort(ar, prep)
				continue
			}
			for _, event := range events {
				// 提问先登记再发布:事件一到前端就可能回答,pending 与 meta 必须已就位;
				// 被忽略的(已有未决提问)不发布,避免出现无法回答的卡片。
				if event.Type == "question_request" {
					if r.recordQuestion(ar, event, lastEntryID) {
						r.publish(ar, event)
					}
					continue
				}
				// 先发布事件再记录审批:自动放行会在记录后立即补发
				// permission_result,不能跑到对应的 escalation_request 前面。
				r.publish(ar, event)
				if event.Type == "escalation_request" {
					r.recordEscalation(ar, event, lastEntryID)
				}
			}
			// 翻译层丢弃的 UI 请求必须回写取消,否则 Pi 会一直等待。
			r.drainCancelUIRequests(ar)
			if eventType(raw) == "agent_settled" {
				r.settleMeta(ar, r.currentPiSessionID(prep.proc))
				return
			}
		case err := <-commandErr:
			if err == nil || ar.stopping.Load() {
				continue
			}
			ar.stateMu.Lock()
			terminal := ar.terminal
			ar.stateMu.Unlock()
			if terminal == "" {
				r.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": err.Error()}})
			}
			// prompt 被拒绝时不会有 agent_settled,直接收尾,由 cleanup 关闭进程。
			return
		case <-ar.waitChanged:
			if waiting := ar.waiting(); waiting && !paused {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				remaining = max(r.cfg.RunTimeout-time.Since(timerStart), time.Millisecond)
				paused = true
			} else if !waiting && paused {
				timer.Reset(remaining)
				timerStart = time.Now().Add(-(r.cfg.RunTimeout - remaining))
				paused = false
			}
		case <-timer.C:
			ar.stateMu.Lock()
			terminal := ar.terminal
			ar.stateMu.Unlock()
			if terminal == "" {
				r.publish(ar, translate.Event{Type: "agent_error", Data: map[string]any{"message": "运行超时"}})
			}
			r.abort(ar, prep)
		case <-ar.ctx.Done():
			r.abort(ar, prep)
		}
	}
}

func (r *Runner) abort(ar *activeRun, prep *prepared) {
	ar.abortOnce.Do(func() {
		go func() {
			if err := prep.proc.Abort(context.Background(), r.cfg.AbortGrace); err != nil {
				r.logger.Warn("中止 Pi 失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
			}
		}()
	})
}

func (r *Runner) recordEscalation(ar *activeRun, event translate.Event, afterEntryID string) {
	data, _ := event.Data["escalation"].(map[string]any)
	id, _ := data["escalation_id"].(string)
	if id == "" {
		return
	}
	escalation := session.Escalation{
		EscalationID: id, AfterEntryID: afterEntryID, At: time.Now().UTC(),
	}
	escalation.ToolName, _ = data["tool_name"].(string)
	escalation.RequestedPath, _ = data["requested_path"].(string)
	escalation.ResolvedPath, _ = data["resolved_path"].(string)
	note, _ := data["note"].(string)
	if command, ok := data["requested_command"].([]string); ok {
		escalation.RequestedCommand = command
	} else if values, ok := data["requested_command"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				escalation.RequestedCommand = append(escalation.RequestedCommand, text)
			}
		}
	}
	// 先登记未决审批再取锚点:事件一经发布用户就可能 Grant,pending 必须尽
	// 快可见,锚点究底要走文件 IO,不能挡在前面。
	ar.stateMu.Lock()
	ar.pending[id] = escalation
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	if escalation.AfterEntryID == "" {
		// Pi 1.4.0 的 entry_appended 不覆盖普通消息条目,事件循环里常拿
		// 不到 id:审批请求到达时,带 toolCall 的 assistant 条目已在工具
		// 执行前落盘,从会话文件末尾取它作本轮锚点。
		escalation.AfterEntryID = r.anchorFromSessionFile(ar)
	}
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		record := escalation
		// 决定可能先于本次落盘到达(Grant 抢在取锚点期间):补写决定,
		// SessionAllow 已由 resolveEscalation 写入,这里不重复。
		ar.stateMu.Lock()
		opts, decided := ar.resolved[id]
		ar.stateMu.Unlock()
		if decided {
			value := opts.granted
			record.Granted = &value
			if opts.scope != "" {
				record.Scope = opts.scope
			}
			if opts.auto {
				record.Auto = true
			}
			if opts.resolvedPath != "" {
				record.ResolvedPath = opts.resolvedPath
			}
		}
		meta.Escalations = append(meta.Escalations, record)
		return nil
	}); err != nil {
		r.logger.Error("记录审批失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	// permission_mode=full 的自动放行由 P3 的 Pi 扩展完成;runner 只传递用户决定。
	// 会话内允许的工具直接放行(读取最新 meta 判断);bash 高危命令的 note
	// 携带「命中规则」提示时不自动放行,仍交用户逐条确认。
	if r.autoGrantable(ar, escalation, note) {
		r.resolveEscalation(ar, id, resolveOptions{granted: true, scope: "session", auto: true})
	}
}

// anchorFromSessionFile 从 Pi 会话文件尾部取最后一个条目 id,作审批与 run
// 失败记录的锚点兜底;读取失败只记日志并返回空串,保持锚点缺失时补到
// transcript 末尾的既有降级。
func (r *Runner) anchorFromSessionFile(ar *activeRun) string {
	id, err := r.cfg.Store.LastEntryID(ar.sessionID)
	if err != nil {
		r.logger.Warn("读取 Pi 会话文件末尾条目失败,锚点留空",
			"session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		return ""
	}
	return id
}

// autoGrantable 判断一条新审批能否依会话内允许自动放行:工具在最新 meta 的
// SessionAllow 里,且不是带高危提示的 bash 审批。
func (r *Runner) autoGrantable(ar *activeRun, escalation session.Escalation, note string) bool {
	if escalation.ToolName == "" {
		return false
	}
	if escalation.ToolName == "bash" && strings.Contains(note, "命中规则") {
		return false
	}
	meta, err := r.cfg.Store.Get(ar.sessionID)
	if err != nil {
		return false
	}
	return slices.Contains(meta.SessionAllow, escalation.ToolName)
}

// resolveOptions 描述一次审批回写:granted/resolvedPath 是决定本体;
// scope="session" 表示会话内允许(允许时把工具写进 meta.SessionAllow);
// auto 表示由会话内允许自动放行,事件里带 auto 标记。
type resolveOptions struct {
	granted      bool
	resolvedPath string
	scope        string
	auto         bool
}

func (r *Runner) resolveEscalation(ar *activeRun, id string, opts resolveOptions) bool {
	ar.stateMu.Lock()
	escalation, ok := ar.pending[id]
	if ok {
		delete(ar.pending, id)
	}
	// 决定同时记在内存里:若审批记录还未落盘(recordEscalation 正在取锚点),
	// 那里的补写逻辑靠它把决定带回 meta,避免刷新后回到未决。
	ar.resolved[id] = opts
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	if !ok {
		return false
	}
	if proc := ar.proc.Load(); proc != nil {
		if err := proc.Send(map[string]any{
			"type": "extension_ui_response", "id": id, "confirmed": opts.granted,
		}); err != nil && !errors.Is(err, pirpc.ErrProcessExited) {
			r.logger.Warn("回写审批失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
	}
	if opts.auto {
		r.publish(ar, ar.tr.AutoDecision(id, opts.granted))
	} else {
		r.publish(ar, ar.tr.Decision(id, opts.granted))
	}
	toolName := escalation.ToolName
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		for i := range meta.Escalations {
			if meta.Escalations[i].EscalationID != id {
				continue
			}
			value := opts.granted
			meta.Escalations[i].Granted = &value
			if opts.scope != "" {
				meta.Escalations[i].Scope = opts.scope
			}
			if opts.auto {
				meta.Escalations[i].Auto = true
			}
			if opts.resolvedPath != "" {
				meta.Escalations[i].ResolvedPath = opts.resolvedPath
			} else if escalation.ResolvedPath != "" {
				meta.Escalations[i].ResolvedPath = escalation.ResolvedPath
			}
		}
		// 会话内允许:把工具加入 allow 列表(去重)
		if opts.granted && opts.scope == "session" && toolName != "" &&
			!slices.Contains(meta.SessionAllow, toolName) {
			meta.SessionAllow = append(meta.SessionAllow, toolName)
		}
		return nil
	}); err != nil {
		r.logger.Error("保存审批决定失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	return true
}

func (r *Runner) findForkEntry(ar *activeRun, text string) (string, error) {
	proc := ar.proc.Load()
	if proc == nil {
		return "", errors.New("runner: Pi 进程尚未启动")
	}
	resp, err := proc.Call(ar.ctx, map[string]any{"type": "get_fork_messages"})
	if err != nil {
		return "", fmt.Errorf("runner: get_fork_messages: %w", err)
	}
	data, err := commandData(resp)
	if err != nil {
		return "", fmt.Errorf("runner: get_fork_messages: %w", err)
	}
	var payload struct {
		Messages []struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("runner: get_fork_messages 应答解析: %w", err)
	}
	for _, message := range payload.Messages {
		if message.Text == text {
			return message.EntryID, nil
		}
	}
	return "", fmt.Errorf("%w: Pi 会话里的用户消息", ErrNotFound)
}

func (r *Runner) fork(ar *activeRun, entryID string) (string, error) {
	proc := ar.proc.Load()
	if proc == nil {
		return "", errors.New("runner: Pi 进程尚未启动")
	}
	resp, err := proc.Call(ar.ctx, map[string]any{"type": "fork", "entryId": entryID})
	if err != nil {
		return "", fmt.Errorf("runner: fork: %w", err)
	}
	data, err := commandData(resp)
	if err != nil {
		return "", fmt.Errorf("runner: fork: %w", err)
	}
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("runner: fork 应答解析: %w", err)
	}
	if result.Cancelled {
		return "", errors.New("runner: fork 被扩展取消")
	}
	id := r.currentPiSessionID(proc)
	if id == "" {
		return "", errors.New("runner: fork 后读取 Pi 会话 id 失败")
	}
	return id, nil
}

func (r *Runner) currentPiSessionID(proc *pirpc.Process) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := proc.Call(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return ""
	}
	data, err := commandData(resp)
	if err != nil {
		return ""
	}
	var state struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return state.SessionID
}

func (r *Runner) settleMeta(ar *activeRun, piSessionID string) {
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		if piSessionID != "" {
			meta.PiSessionID = piSessionID
		}
		file, err := session.PiSessionFile(r.cfg.Store.PiDir(ar.sessionID), meta.PiSessionID)
		if err != nil {
			return nil
		}
		ids, err := userEntryIDs(file)
		if err != nil {
			return nil
		}
		bound := map[string]bool{}
		for _, message := range meta.Messages {
			if message.EntryID != "" {
				bound[message.EntryID] = true
			}
		}
		fresh := make([]string, 0, len(ids))
		for _, id := range ids {
			if !bound[id] {
				fresh = append(fresh, id)
			}
		}
		for i := range meta.Messages {
			if len(fresh) == 0 {
				break
			}
			if meta.Messages[i].EntryID == "" {
				meta.Messages[i].EntryID = fresh[0]
				fresh = fresh[1:]
			}
		}
		return nil
	}); err != nil {
		r.logger.Error("更新会话绑定失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
}

func userEntryIDs(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var ids []string
	for {
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var entry struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Message struct {
					Role string `json:"role"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "message" && entry.ID != "" && entry.Message.Role == "user" {
				ids = append(ids, entry.ID)
			}
		}
		if err != nil {
			break
		}
	}
	return ids, nil
}

func appendedEntryID(raw []byte) string {
	var event struct {
		Type  string `json:"type"`
		Entry struct {
			ID string `json:"id"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &event) == nil && event.Type == "entry_appended" {
		return event.Entry.ID
	}
	return ""
}

func eventType(raw []byte) string {
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &event)
	return event.Type
}

func commandData(resp json.RawMessage) (json.RawMessage, error) {
	var result struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	if !result.Success {
		if result.Error == "" {
			result.Error = "命令被拒绝"
		}
		return nil, errors.New(result.Error)
	}
	return result.Data, nil
}

func jsonUnmarshal(data json.RawMessage, target any) error {
	if len(data) == 0 || string(data) == "null" {
		return errors.New("应答缺少 data")
	}
	return json.Unmarshal(data, target)
}

func sanitizeStderr(stderr string, prep *prepared, max int) string {
	// stderr 不属于协议,可能夹带诊断里的敏感值;统一去空白、脱敏本 run 签发
	// 过的全部令牌(含续期被替换的旧令牌)并限制长度。
	text := strings.Join(strings.Fields(stderr), " ")
	if prep != nil {
		for _, token := range prep.tokenList() {
			text = strings.ReplaceAll(text, token, "***")
		}
	}
	if len(text) > max {
		text = text[:max]
	}
	return text
}
