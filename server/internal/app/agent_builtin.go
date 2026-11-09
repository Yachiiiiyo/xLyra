package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"xlyra/server/internal/agent"
	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/execd"
	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/runner"
	"xlyra/server/internal/agent/sandbox"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/config"
	routeengine "xlyra/server/internal/router"
)

// 内置 Agent 的工具白名单:隔离成立(或显式关闭)时全开;降级时只留只读
// (Q9:无法切 uid 时写类工具 fail-closed)。
var (
	fullAgentTools     = []string{"read", "grep", "find", "ls", "write", "edit"}
	readOnlyAgentTools = []string{"read", "grep", "find", "ls"}
)

// selfCheckFn 与 isolation.SelfCheck 同签名,是集成测试注入失败用的钩子。
var selfCheckFn = isolation.SelfCheck

// execSelfCheckFn 与 execd.SelfCheck 同签名,供集成测试注入沙箱自检失败。
var execSelfCheckFn = execd.SelfCheck

const (
	// selfCheckTimeout 是整个自检(含重试)的墙钟上限。
	selfCheckTimeout = 2 * time.Minute
	// selfCheckRetryDelay 是自检失败后的重试等待:自检是确定性的权限检查,
	// 偶发环境抖动不应让 Agent 被误判不可用。
	selfCheckRetryDelay = time.Second
)

// builtinAgent 是内置 Agent(pi 引擎)的装配结果:HTTP handler 与关停钩子。
type builtinAgent struct {
	handler  *agent.Handler
	shutdown func(context.Context)
	// beginRestore / endRestore 在备份恢复前后拦住 Agent 运行;Agent 不可用
	// (runner 未构造)时为 nil。
	beginRestore func() error
	endRestore   func()
	// relaxWorkspace 在 uid 隔离开启时放开共享工作区已有内容的权限(备份恢复出的
	// 文件属主是 xLyra 进程用户,各会话身份需要能读写);隔离未开启时为 nil。
	relaxWorkspace func()
}

// agentBins 是 node 命令、Pi 入口与 xLyra 扩展的解析结果;任一为空表示
// 内置 Agent 未就绪。
type agentBins struct {
	command   string
	piEntry   string
	extension string
}

func (b agentBins) ready() bool {
	return b.command != "" && b.piEntry != "" && b.extension != ""
}

// newBuiltinAgent 按配置装配内置 Agent(session 存储、能力存储、run 事件流、
// 隔离启动流程、runner 与 HTTP handler)。node 命令或 Pi 入口脚本缺失时不
// 报错:handler 正常路由,health 会报告不可用原因。
func newBuiltinAgent(cfg config.Config, logger *slog.Logger, confFile *config.ConfigFile, routerService *routeengine.Service, tokens runner.TokenIssuer, modelMemory http.HandlerFunc) *builtinAgent {
	dataDir := resolveAgentDataDir(cfg)
	// 能力存储与 sessions 同级:<dataDir>/skills、AGENTS.md、config.json。
	caps := capabilities.NewStore(dataDir)
	workspaceDir := resolveAgentWorkspaceDir(cfg, dataDir)
	store := session.NewStore(filepath.Join(dataDir, "sessions")).SetWorkspace(workspaceDir)
	if err := os.MkdirAll(workspaceDir, 0o750); err != nil {
		logger.Warn("创建共享工作区目录失败", "workspace", workspaceDir, "error", err)
	}
	if moved, err := store.MigrateLegacyWorkspaces(); err != nil {
		logger.Warn("迁移旧会话工作区失败", "error", err)
	} else if moved > 0 {
		logger.Info("已把旧会话工作区迁入共享工作区 _legacy 目录", "moved", moved)
	}
	runs := runlog.NewRegistry()

	bins := resolveAgentBins(cfg)
	decision := prepareIsolation(cfg, logger, dataDir, caps, bins)
	if decision.Isolation != nil {
		// uid 隔离开启时共享工作区需要全员可读写(靠 Landlock+uid 隔离而非目录权限保护)。
		if err := isolation.PrepareWorkspace(workspaceDir); err != nil {
			logger.Warn("共享工作区权限准备失败", "workspace", workspaceDir, "error", err)
		}
	}

	run, shutdown := newBuiltinRunner(cfg, logger, confFile, routerService, store, runs, tokens, caps, decision, bins)
	var suggest func(context.Context, string, string, string, string, int) (string, error)
	if completer, ok := tokens.(interface {
		CompleteText(context.Context, string, string, string, string, int) (string, error)
	}); ok {
		suggest = completer.CompleteText
	}
	handler := agent.NewHandler(agent.Deps{
		Runner:       run,
		Store:        store,
		Runs:         runs,
		Capabilities: caps,
		ModelMemory:  modelMemory,
		Suggest:      suggest,
		SuggestionEnabled: func() bool {
			return cfg.AgentPromptSuggestion && agentproxy.PromptSuggestionEnabledFromConfig(confFile)
		},
		FastModel: func() string {
			return agentproxy.FastModelFromConfig(confFile)
		},
		ValidateAttachments: agentproxy.ValidateAttachments,
		Logger:              logger,
		MaxBodyBytes:        cfg.MaxRequestBodyBytes,
		Status:              func() agent.Status { return decision.status() },
	})
	agentInstance := &builtinAgent{handler: handler, shutdown: shutdown}
	if gate, ok := run.(interface {
		BeginRestore() error
		EndRestore()
	}); ok {
		agentInstance.beginRestore = gate.BeginRestore
		agentInstance.endRestore = gate.EndRestore
	}
	if decision.Isolation != nil {
		agentInstance.relaxWorkspace = func() {
			if err := isolation.RelaxWorkspacePermissions(workspaceDir); err != nil {
				logger.Warn("放开共享工作区权限失败", "workspace", workspaceDir, "error", err)
			}
		}
	}
	return agentInstance
}

// resolveAgentDataDir 返回内置 Agent 的数据目录，供运行时装配与备份共用。
func resolveAgentDataDir(cfg config.Config) string {
	return config.ResolveAgentDataDir(cfg)
}

// resolveAgentWorkspaceDir 返回共享工作区目录:配置了 AGENT_WORKSPACE_DIR
// 则用之,否则 <数据目录>/workspace;用 filepath.Abs 规整(失败则原样返回)。
func resolveAgentWorkspaceDir(cfg config.Config, dataDir string) string {
	return config.ResolveAgentWorkspaceDir(cfg)
}

// resolveAgentBins 解析 node 命令、Pi 入口与扩展路径;command 缺省时回退
// PATH 查找,扩展从 Pi 入口推导(见 agentExtensionPath)。
func resolveAgentBins(cfg config.Config) agentBins {
	command := strings.TrimSpace(cfg.AgentNodeBin)
	if command == "" {
		if resolved, err := exec.LookPath("node"); err == nil {
			command = resolved
		}
	}
	piEntry := strings.TrimSpace(cfg.AgentPiEntry)
	return agentBins{command: command, piEntry: piEntry, extension: agentExtensionPath(cfg.AgentExtensionPath, piEntry)}
}

// isolationDecision 是隔离启动流程的决策结果(纯函数 decideIsolation 产出,
// 便于表驱动单测)。
type isolationDecision struct {
	// Mode 是隔离模式:"on" | "degraded" | "off" | "failed";空表示未评估
	// (Pi 未就绪时不做探测)。
	Mode      string
	Reason    string // 中文说明(给 Health 与日志)
	Isolation *runner.IsolationConfig
	Tools     []string // 该模式下的 --tools 白名单
	Sandbox   *sandboxDecision
}

// status 把决策转换为 handler 的只读状态输出。
func (d isolationDecision) status() agent.Status {
	write := false
	for _, tool := range d.Tools {
		if tool == "write" || tool == "edit" {
			write = true
		}
	}
	st := agent.Status{Mode: d.Mode, Reason: d.Reason, WriteToolsEnabled: write}
	if d.Sandbox != nil {
		st.Sandbox = d.Sandbox.Mode
		st.SandboxReason = d.Sandbox.Reason
		st.BashEnabled = d.Sandbox.Bash
	}
	return st
}

// decideIsolation 按"模式 × 探测 × 自检"给出隔离决策(fail-closed):
//
//   - off:不隔离,工具全开(仅本地开发);
//   - auto:环境不支持 uid 隔离时降级为只读工具;
//   - required:环境不支持时 Agent 整体关闭;
//   - 探测通过但自检未通过:两种模式都关闭 Agent——环境明明能切 uid 却
//     隔离不成立,说明部署有误(Pi 能读到 xLyra 的东西),绝不降级运行。
func decideIsolation(mode string, sandboxEnabled bool, detect isolation.Capability, check *isolation.Report) isolationDecision {
	if mode == "off" {
		return isolationDecision{
			Mode:   "off",
			Reason: "AGENT_ISOLATION=off,Agent 与 xLyra 同权限运行,仅用于本地开发",
			Tools:  fullAgentTools,
		}
	}
	// 沙箱未开启时 auto 直接不隔离:Agent 与 xLyra 同权限运行,命令可联网;
	// required(含未知值)仍然强制沙箱,不看开关。
	if mode == "auto" && !sandboxEnabled {
		return isolationDecision{
			Mode:   "off",
			Reason: "沙箱未启用（可在 Agent 设置中开启），Agent 与 xLyra 同权限运行，命令直接在本机执行",
			Tools:  fullAgentTools,
		}
	}
	if mode != "auto" { // required 及未知值都按最严格的 fail-closed 处理
		mode = "required"
	}
	if !detect.OK {
		if mode == "auto" {
			return isolationDecision{Mode: "degraded", Reason: detect.Reason, Tools: readOnlyAgentTools}
		}
		return isolationDecision{Mode: "failed", Reason: "AGENT_ISOLATION=required 但环境不支持 uid 隔离:" + detect.Reason}
	}
	if check == nil {
		return isolationDecision{Mode: "failed", Reason: "隔离自检未执行"}
	}
	if !check.OK {
		return isolationDecision{Mode: "failed", Reason: "隔离自检失败:" + strings.Join(failedCheckNames(*check), "、")}
	}
	return isolationDecision{Mode: "on", Isolation: &runner.IsolationConfig{}, Tools: fullAgentTools}
}

// failedCheckNames 汇总报告里未通过的检查项名(不含 Detail,避免敏感内容)。
func failedCheckNames(rep isolation.Report) []string {
	var names []string
	for _, c := range rep.Checks {
		if !c.Passed {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		names = []string{"未知检查项"}
	}
	return names
}

// prepareIsolation 执行 uid 隔离的启动流程(08 §八 P5a、§十六 16.7):
// 探测 → 一次性修权限 → 收紧 WORKDIR → 自检(失败重试一次)→ 决策。
// AGENT_ISOLATION=off 时只打 WARN 不做任何探测;Pi 入口/扩展未就绪时不做
// 任何权限变更(反正 runner 不可用),返回未评估的空决策。
func prepareIsolation(cfg config.Config, logger *slog.Logger, dataDir string, caps *capabilities.Store, bins agentBins) isolationDecision {
	mode := strings.TrimSpace(cfg.AgentIsolation)
	if mode == "" {
		mode = "auto"
	}
	if mode == "off" {
		logger.Warn("AGENT_ISOLATION=off,Agent 与 xLyra 同权限运行,仅用于本地开发")
		decision := decideIsolation(mode, false, isolation.Capability{}, nil)
		sandbox := decideSandbox(cfg.AgentBash, decision, sandbox.Capability{}, nil)
		decision.Sandbox = &sandbox
		return decision
	}
	if !bins.ready() {
		return isolationDecision{}
	}
	// Agent 设置里的沙箱开关(默认关闭):未开启时不做任何探测与自检,
	// 命令直接在本机执行;读取失败按未开启处理。
	sandboxEnabled := false
	if caps != nil {
		capsCfg, err := caps.GetConfig()
		if err != nil {
			logger.Warn("读取 Agent 配置失败,按沙箱未启用处理", "error", err)
		} else {
			sandboxEnabled = capsCfg.SandboxEnabled
		}
	}
	if mode == "auto" && !sandboxEnabled {
		logger.Info("沙箱未启用，命令直接在本机执行")
		decision := decideIsolation(mode, sandboxEnabled, isolation.Capability{}, nil)
		sandbox := decideSandbox(cfg.AgentBash, decision, sandbox.Capability{}, nil)
		decision.Sandbox = &sandbox
		return decision
	}
	isoCfg := isolation.Config{UIDBase: cfg.AgentUIDBase, MaxSlots: cfg.AgentMaxSlots}
	// Detect 的探针身份要走进数据目录才能读回文件,而旧部署里数据目录可能是 0750
	// (设置写入 AGENTS.md/config.json 时由 MkdirAll 建出)。先把它修成可穿越再探测,
	// 否则会被误判成"环境不支持"而静默降级。非 root 时这两步返回 ErrNotRoot 或无权
	// 修改,忽略即可,Detect 会给出真正的原因。
	if os.Geteuid() == 0 {
		if err := isolation.PrepareDataDir(dataDir); err != nil {
			logger.Warn("探测前准备数据目录失败", "data_dir", dataDir, "error", err)
		}
		if err := isolation.EnsureTraversable(config.ResolveWorkdir(), dataDir); err != nil {
			logger.Warn("探测前确保数据目录可穿越失败", "data_dir", dataDir, "error", err)
		}
	}
	detect := isolation.Detect(dataDir, isoCfg)
	var check *isolation.Report
	var hardened []string
	if detect.OK {
		logger.Info("uid 隔离能力探测通过")
		fixupDataDir(logger, caps, dataDir)
		hardened = tightenWorkdir(logger, dataDir, bins)
		check = selfCheckWithRetry(logger, caps, dataDir, isoCfg, hardened)
	} else {
		logger.Warn("uid 隔离能力探测未通过,按降级策略处理", "reason", detect.Reason)
	}
	decision := decideIsolation(mode, sandboxEnabled, detect, check)
	if decision.Isolation != nil {
		decision.Isolation.Config = isoCfg
	}
	if decision.Mode == "on" {
		sandboxDecision := prepareSandbox(cfg, logger, dataDir, decision, isoCfg, hardened)
		decision.Sandbox = &sandboxDecision
	} else {
		sandboxDecision := decideSandbox(cfg.AgentBash, decision, sandbox.Capability{}, nil)
		decision.Sandbox = &sandboxDecision
	}
	if decision.Mode == "failed" {
		logger.Error("uid 隔离不可用,内置 Agent 已按 fail-closed 关闭", "reason", decision.Reason)
	} else {
		logger.Info("uid 隔离决策完成", "mode", decision.Mode,
			"reason", decision.Reason, "tools", strings.Join(decision.Tools, ","))
	}
	return decision
}

// fixupDataDir 做旧部署的一次性修权限:数据目录与 sessions 根为 root:root
// 0711,技能目录树统一 0755/0644(会话身份要能读技能)。
func fixupDataDir(logger *slog.Logger, caps *capabilities.Store, dataDir string) {
	if err := isolation.PrepareDataDir(dataDir); err != nil {
		logger.Error("Agent 数据目录准备失败", "data_dir", dataDir, "error", err)
	}
	if caps == nil {
		return
	}
	changed, err := caps.FixPermissions()
	if err != nil {
		logger.Warn("技能目录权限修正失败", "error", err)
		return
	}
	if changed > 0 {
		logger.Info("已修正技能目录权限(目录 0755/文件 0644)", "changed", changed)
	}
}

// tightenWorkdir 收紧 WORKDIR 的直接子项(去掉组/其他读写),并给 WORKDIR
// 本身与到 Agent 数据目录之间的目录补上"其他用户可穿越"位。排除两类:
// Agent 数据目录整条路径;Pi 入口/扩展/node 命令所在的顶层条目——会话
// 身份要读 Pi 运行时并执行 node,收紧了它们 Agent 就起不来。返回被收紧的
// 路径(供自检的 SecretPaths 与日志)。
func tightenWorkdir(logger *slog.Logger, dataDir string, bins agentBins) []string {
	workdir := config.ResolveWorkdir()
	dataDir = filepath.Clean(dataDir)
	fi, err := os.Stat(workdir)
	if err != nil || !fi.IsDir() {
		logger.Warn("WORKDIR 不存在或不是目录,跳过权限收紧", "workdir", workdir)
		return nil
	}
	entries, err := os.ReadDir(workdir)
	if err != nil {
		logger.Warn("无法列出 WORKDIR 子项,跳过权限收紧", "workdir", workdir, "error", err)
		return nil
	}
	// Agent 数据目录本身或其祖先(WORKDIR 之下的整条路径)不收紧
	onAgentPath := func(p string) bool {
		return p == dataDir || strings.HasPrefix(dataDir, p+string(filepath.Separator))
	}
	keepOpen := map[string]bool{}
	for _, path := range []string{bins.command, bins.piEntry, bins.extension} {
		if top, ok := topLevelUnder(workdir, path); ok {
			keepOpen[top] = true
		}
	}
	var targets []string
	for _, entry := range entries {
		p := filepath.Join(workdir, entry.Name())
		if onAgentPath(p) || keepOpen[p] {
			continue
		}
		targets = append(targets, p)
	}
	// 配置目录里的文件(master.key、含站点凭据的配置)本身也要是仅 root 可访问:
	// 目录 0700 已经挡住其他用户,但文件自己的权限是第二层防御,也是它们的设计模式。
	if confEntries, readErr := os.ReadDir(config.ConfigDir(workdir)); readErr == nil {
		for _, entry := range confEntries {
			targets = append(targets, filepath.Join(config.ConfigDir(workdir), entry.Name()))
		}
	}
	changed, err := isolation.Harden(targets)
	if err != nil {
		logger.Warn("WORKDIR 子项收紧未完成", "error", err, "changed", changed)
	}
	if len(changed) > 0 {
		logger.Info("已收紧 WORKDIR 子项权限(去掉组/其他读写)", "paths", changed)
	}
	if err := isolation.EnsureTraversable(workdir, dataDir); err != nil {
		logger.Warn("确保数据目录路径可穿越失败", "workdir", workdir, "data_dir", dataDir, "error", err)
	}
	return changed
}

// topLevelUnder 返回 path 在 root 之下的顶层条目(WORKDIR 的直接子项);
// path 不在 root 之下时 ok=false。
func topLevelUnder(root, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	return filepath.Join(root, first), true
}

// selfCheckWithRetry 在实际数据目录上执行隔离自检,失败后等 1 秒重试一次
// (自检是确定性的权限检查,真正的错误配置两次都会失败);两次报告都
// 记日志(失败项名与详情),任一次通过即通过。
func selfCheckWithRetry(logger *slog.Logger, caps *capabilities.Store, dataDir string, isoCfg isolation.Config, hardened []string) *isolation.Report {
	workdir := config.ResolveWorkdir()
	secret := append([]string(nil), hardened...)
	secret = append(secret, config.ConfigDir(workdir), config.MasterKeyPath(workdir))
	in := isolation.SelfCheckInput{
		DataDir:       dataDir,
		SecretPaths:   secret,
		ReadableProbe: readableSkillProbe(caps, dataDir),
		SelfPID:       os.Getpid(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfCheckTimeout)
	defer cancel()
	var rep isolation.Report
	for attempt := 1; attempt <= 2; attempt++ {
		rep = selfCheckFn(ctx, isoCfg, in)
		if rep.OK {
			logger.Info("uid 隔离自检通过", "attempt", attempt)
			return &rep
		}
		logFailedReport(logger, "uid 隔离", attempt, rep)
		if attempt == 1 {
			time.Sleep(selfCheckRetryDelay)
		}
	}
	return &rep
}

// logFailedReport 记录自检失败报告(失败项名与详情;Detail 由 isolation
// 包保证不含文件内容)。
func logFailedReport(logger *slog.Logger, kind string, attempt int, rep isolation.Report) {
	failed := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		if !c.Passed {
			failed = append(failed, c.Name+": "+c.Detail)
		}
	}
	logger.Error(kind+"自检未通过", "attempt", attempt, "failed", failed)
}

// readableSkillProbe 返回技能目录下任意一个合法 SKILL.md 作为自检的正向
// 可读探针;没有可用技能时返回空(跳过该项)。
func readableSkillProbe(caps *capabilities.Store, dataDir string) string {
	if caps == nil {
		return ""
	}
	info, err := caps.ListSkills()
	if err != nil || len(info.Skills) == 0 {
		return ""
	}
	return filepath.Join(dataDir, info.Skills[0].Path, "SKILL.md")
}

// sandboxDecision 是沙箱与 bash 的独立降级结果;失败不影响 uid 隔离。
type sandboxDecision struct {
	Mode       string
	Reason     string
	PiLandlock bool
	Bash       bool
	Exe        string
}

// decideSandbox 按 uid 隔离、能力探测与执行器自检决定沙箱和 bash 开关。
func decideSandbox(bashMode string, iso isolationDecision, detect sandbox.Capability, check *isolation.Report) sandboxDecision {
	// 隔离关闭(环境变量 off 或沙箱开关未开)时命令直接在本机执行,
	// bash 仅受 AGENT_BASH=off 控制;降级/失败等其余情况仍然 bash 关闭。
	if iso.Mode == "off" {
		if bashMode == "off" {
			return sandboxDecision{Mode: "off", Reason: "AGENT_BASH=off,bash 已关闭"}
		}
		return sandboxDecision{Mode: "off", Bash: true, Reason: "沙箱未启用,命令直接在本机执行"}
	}
	if iso.Mode != "on" {
		return sandboxDecision{Mode: "off", Reason: "uid 隔离未启用,沙箱与 bash 关闭"}
	}
	if !detect.OK {
		return sandboxDecision{Mode: "unavailable", Reason: detect.Reason}
	}
	if check == nil || !check.OK {
		reason := "沙箱自检失败:未知检查项"
		if check != nil {
			reason = "沙箱自检失败:" + strings.Join(failedCheckNames(*check), "、")
		}
		return sandboxDecision{Mode: "failed", Reason: reason}
	}
	decision := sandboxDecision{Mode: "on", PiLandlock: true, Bash: bashMode != "off"}
	if bashMode == "off" {
		decision.Reason = "AGENT_BASH=off,bash 已关闭"
	}
	return decision
}

// execSocketPathLength 是最长会话执行器套接字路径的字节数;Linux 上限为
// 108 字节且实现需要以 NUL 结尾,这里沿用执行器的 104 字节保守上限。
func execSocketPathLength(dataDir string) int {
	return len(dataDir) + len("/sessions/") + 36 + len("/exec/exec.sock")
}

// prepareSandbox 在 uid 隔离成立后清理旧进程并验证沙箱。沙箱失败只关闭
// Landlock 与 bash,不把整个 Agent 置为不可用。
func prepareSandbox(cfg config.Config, logger *slog.Logger, dataDir string, iso isolationDecision, isoCfg isolation.Config, hardened []string) sandboxDecision {
	if os.Geteuid() == 0 {
		// 与身份分配用同一份规整后的参数,否则零值配置下会扫到空区间
		pool := isoCfg.Normalize()
		killed := sandbox.SweepUIDs(pool.UIDBase, 2*pool.MaxSlots)
		logger.Info("已清理 Agent 身份池残留进程", "killed", killed)
	}
	detect := sandbox.Detect()
	var check *isolation.Report
	var exe string
	if detect.OK {
		var err error
		exe, err = os.Executable()
		if err != nil {
			detect = sandbox.Capability{Reason: "获取可执行文件失败:" + err.Error()}
		} else if pathLen := execSocketPathLength(dataDir); pathLen > 104 {
			decision := decideSandbox(cfg.AgentBash, iso, detect, &isolation.Report{Checks: []isolation.Check{{
				Name: "套接字路径", Detail: "路径过长",
			}}})
			decision.Reason = fmt.Sprintf("Agent 数据目录路径过长(套接字路径 %d 字节,上限 104 字节),套接字无法创建,请缩短 AGENT_DATA_DIR/数据目录路径", pathLen)
			logger.Error("沙箱自检失败", "reason", decision.Reason)
			return decision
		} else {
			check = execSelfCheckWithRetry(logger, dataDir, isoCfg, hardened, exe)
		}
	}
	decision := decideSandbox(cfg.AgentBash, iso, detect, check)
	decision.Exe = exe
	switch decision.Mode {
	case "on":
		logger.Info("沙箱决策完成", "mode", decision.Mode, "reason", decision.Reason, "bash", decision.Bash)
	case "failed":
		logger.Error("沙箱不可用,已关闭 Landlock 与 bash", "reason", decision.Reason)
	default:
		logger.Warn("沙箱降级", "mode", decision.Mode, "reason", decision.Reason)
	}
	return decision
}

// execSelfCheckWithRetry 与 uid 隔离自检共用墙钟上限及一次重试策略。
func execSelfCheckWithRetry(logger *slog.Logger, dataDir string, isoCfg isolation.Config, hardened []string, exe string) *isolation.Report {
	workdir := config.ResolveWorkdir()
	secret := append([]string(nil), hardened...)
	secret = append(secret, config.ConfigDir(workdir), config.MasterKeyPath(workdir))
	in := execd.CheckInput{DataDir: dataDir, SecretPaths: secret, Exe: exe, Isolation: isoCfg}
	ctx, cancel := context.WithTimeout(context.Background(), selfCheckTimeout)
	defer cancel()
	var rep isolation.Report
	for attempt := 1; attempt <= 2; attempt++ {
		rep = execSelfCheckFn(ctx, in)
		if rep.OK {
			logger.Info("沙箱自检通过", "attempt", attempt)
			return &rep
		}
		logFailedReport(logger, "沙箱", attempt, rep)
		if attempt == 1 {
			time.Sleep(selfCheckRetryDelay)
		}
	}
	return &rep
}

// newBuiltinRunner 解析 node 命令与 Pi 入口并构造 runner;隔离决策为 failed
// 或任一入口缺失时返回降级实现(所有操作不可用),保证服务仍可启动。
func newBuiltinRunner(cfg config.Config, logger *slog.Logger, confFile *config.ConfigFile, routerService *routeengine.Service, store *session.Store, runs *runlog.Registry, tokens runner.TokenIssuer, caps runner.CapabilitiesSource, decision isolationDecision, bins agentBins) (agent.Runner, func(context.Context)) {
	reason := ""
	switch {
	case bins.command == "":
		reason = "未配置 node 命令(AGENT_NODE_BIN)"
	case bins.piEntry == "":
		reason = "未配置 Pi 入口脚本(AGENT_PI_ENTRY)"
	case decision.Mode == "failed":
		// fail-closed:自检不通过绝不降级运行 Pi(部署有误时 Pi 能读到
		// xLyra 的数据),直接不构造 runner。
		reason = "Agent 隔离不可用,已按 fail-closed 关闭:" + decision.Reason
	}
	if reason != "" {
		logger.Warn("内置 Agent 不可用", "reason", reason)
		return unavailableRunner{reason: reason}, func(context.Context) {}
	}
	gatewayBaseURL := strings.TrimSpace(cfg.AgentGatewayBaseURL)
	if gatewayBaseURL == "" {
		gatewayBaseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort)
	}
	rc := runner.Config{
		Store:             store,
		Runs:              runs,
		Tokens:            tokens,
		Protocol:          agentProtocolResolver(logger, confFile, routerService),
		ImageModels:       func() int { return len(agentproxy.ImageModelsFromConfig(confFile)) },
		ClassifierModel:   func() string { return agentproxy.ClassifierModelFromConfig(confFile) },
		FastModel:         func() string { return agentproxy.FastModelFromConfig(confFile) },
		FastTasks:         true,
		GatewayBaseURL:    gatewayBaseURL,
		Command:           bins.command,
		Args:              []string{bins.piEntry},
		Tools:             decision.Tools,
		Capabilities:      caps,
		ExtensionPath:     bins.extension,
		PathEnv:           filepath.Dir(bins.command) + ":/usr/bin:/bin",
		MaxConcurrent:     cfg.AgentMaxConcurrent,
		RunTimeout:        cfg.AgentRunTimeout,
		QuestionTimeout:   cfg.AgentQuestionTimeout,
		MaxOutputBytes:    cfg.AgentMaxOutputBytes,
		SessionQuotaBytes: cfg.AgentSessionQuotaBytes,
		QueueTimeout:      cfg.AgentQueueTimeout,
		QueueMax:          cfg.AgentQueueMax,
		Isolation:         decision.Isolation,
		Logger:            logger,
	}
	if completer, ok := tokens.(interface {
		CompleteTextKind(context.Context, string, string, string, string, int, string) (string, error)
	}); ok {
		rc.Classify = func(ctx context.Context, sessionID, model, system, user string, maxTokens int) (string, error) {
			return completer.CompleteTextKind(ctx, sessionID, model, system, user, maxTokens, "permission_classifier")
		}
		rc.CompleteText = completer.CompleteTextKind
	}
	if decision.Sandbox != nil && decision.Sandbox.Mode == "on" {
		rc.Sandbox = &runner.SandboxConfig{
			Exe:                decision.Sandbox.Exe,
			PiLandlock:         decision.Sandbox.PiLandlock,
			Bash:               decision.Sandbox.Bash,
			ExecDefaultTimeout: cfg.AgentExecDefaultTimeout,
			ExecMaxTimeout:     cfg.AgentExecMaxTimeout,
		}
	}
	// 沙箱关闭但 bash 开放:命令在本机直接执行,不经沙箱执行器。
	if decision.Sandbox != nil && decision.Sandbox.Mode == "off" && decision.Sandbox.Bash {
		local := runner.LocalBashConfig{
			DefaultTimeout: cfg.AgentExecDefaultTimeout,
			MaxTimeout:     cfg.AgentExecMaxTimeout,
		}
		if local.DefaultTimeout <= 0 {
			local.DefaultTimeout = 120 * time.Second
		}
		if local.MaxTimeout <= 0 {
			local.MaxTimeout = 600 * time.Second
		}
		if local.DefaultTimeout > local.MaxTimeout {
			local.DefaultTimeout = local.MaxTimeout
		}
		rc.LocalBash = &local
	}
	if cfg.AgentNodeMaxHeapMB > 0 {
		rc.NodeFlags = []string{fmt.Sprintf("--max-old-space-size=%d", cfg.AgentNodeMaxHeapMB)}
	}
	run, err := runner.New(rc)
	if err != nil {
		logger.Warn("内置 Agent 启动失败", "error", err)
		return unavailableRunner{reason: err.Error()}, func(context.Context) {}
	}
	return run, func(ctx context.Context) {
		if err := run.Shutdown(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("内置 Agent 关停未完成", "error", err)
		}
	}
}

// agentExtensionPath 解析 xLyra 扩展路径:显式配置优先;否则从 Pi 入口推导
// ——入口通常在 <root>/agent-runtime/node_modules/@earendil-works/pi-coding-agent/
// dist/bundle/cli.js,扩展约定为 <root>/agent-runtime/extension/xlyra.ts(向上
// 找名为 node_modules 的祖先目录,取其父目录)。推导不到或文件不存在时返回空,
// 由 runner.Health 报告不可用(写类工具 fail-closed)。
func agentExtensionPath(configured, piEntry string) string {
	if path := strings.TrimSpace(configured); path != "" {
		return path
	}
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(piEntry)))
	for {
		if filepath.Base(dir) == "node_modules" {
			candidate := filepath.Join(filepath.Dir(dir), "extension", "xlyra.ts")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// unavailableRunner 是缺配置时的降级实现:health 给出原因,其余操作一律
// 返回 ErrUnavailable。
type unavailableRunner struct {
	reason string
}

func (u unavailableRunner) Send(context.Context, runner.SendInput) (runner.SendResult, error) {
	return runner.SendResult{}, fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

func (u unavailableRunner) Retry(context.Context, runner.RetryInput) (runner.SendResult, error) {
	return runner.SendResult{}, fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

func (u unavailableRunner) Compact(context.Context, string, string) (runner.CompactResult, error) {
	return runner.CompactResult{}, fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

func (u unavailableRunner) Stop(context.Context, string) error {
	return fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

func (u unavailableRunner) Grant(context.Context, string, runner.GrantInput) error {
	return fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

func (u unavailableRunner) AnswerQuestion(context.Context, runner.AnswerQuestionInput) error {
	return fmt.Errorf("%w: %s", runner.ErrUnavailable, u.reason)
}

// PendingCounts 降级实现无活跃 run,恒为 0。
func (u unavailableRunner) PendingCounts(string) (int, int) {
	return 0, 0
}

func (u unavailableRunner) Health() runner.Health {
	return runner.Health{Reason: u.reason}
}
