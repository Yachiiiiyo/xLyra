---
name: xlyra
description: Use the xlyra control-plane CLI via the bash tool to inspect and safely configure the running xLyra gateway, sites, models, routes, cooldowns, proxies, rate limits, general/portal/appearance/backup settings, dashboards, request logs, usage metrics, and audit logs.
---

# xlyra 控制面使用指南

`xlyra` CLI 已预置于运行环境的 `PATH` 中，并通过隔离的 Unix Domain Socket（`XLYRA_CTL_SOCK`）直连 xLyra 主程序控制面。无需手写 curl 或猜配置文件路径。

## 执行规范（重要）

1. **保持单条纯净命令**：始终独立调用单条 `xlyra ...` 命令，**不要**拼接 `&&`、`||`、`|`、`;`、`$()` 或反引号。纯净的单条 `xlyra` 命令会走控制面直通通道：读操作零弹窗，写操作仅弹一次结构化 Diff 审批。
2. **先自省与预览**：修改配置或站点状态前，先读取当前值（获取 `revision`），再用 `--dry-run` 确认变更范围，必要时传 `--if-match <revision>` 防止并发覆盖。
3. **收窄大数据输出**：响应体积上限为 32 KiB，超限会截断并设置 `meta.truncated=true`。查询请求日志、模型或价格列表时优先使用 `--fields`、`-q`、`--page`、`--page-size` 过滤。
4. **尊重 Tier 0 安全红线**：
   - Agent 自身配置（`agent.settings.*`、`agent.capabilities.*`、`AGENTS.md`、`skills/*`）仅限只读诊断，禁止写入。
   - 外部明文密钥（`api-key reveal`、`site-key create`、代理 URL 含密码、`backup.auto` 密钥字段、`backup restore/import`、`cloud bind/unbind`）由服务端 Tier 0 拦截，请引导用户在 Web 控制台操作。

## 常用命令速查

### 元数据与状态
- `xlyra status` — 查看主程序版本、权限模式、功能开关与当前 `config_revision`
- `xlyra schema` — 列出所有可用资源、动作、域与风险等级（Tier 0–3）
- `xlyra schema <resource|domain>` — 查看指定资源或域下的全部动作说明

### 全局与单例配置（general / portal / appearance / rate-limit / backup.auto）
- `xlyra config list` — 列出所有配置命名空间
- `xlyra config describe <namespace>` — 查看命名空间下所有配置键与风险等级
- `xlyra config get [path]` — 读取配置（例如 `general`、`general.log.level`、`portal`、`rate-limit`、`agent.settings`）
- `xlyra config set <key>=<value> [<key>=<value>...] [--dry-run] [--if-match <rev>]`
  - 示例：`xlyra config set general.log.level=debug --dry-run`
  - 示例：`xlyra config set rate-limit.status=enabled rate-limit.rpm_limit=120`
  - 示例：`xlyra config set portal.enabled=true portal.summary_days=7`
- `xlyra config patch <namespace> --json '<json>' [--dry-run]`
- `xlyra config reset <namespace> [--dry-run]` — 将可写配置域重置为默认值（Tier 3）

### 网关、站点、模型与路由
- `xlyra proxy list` / `proxy get <id|name>` / `proxy create --name N --type http|https|socks5 --url U` / `proxy update <id|name> ...` / `proxy delete <id|name>`
- `xlyra site list [--enabled true|false] [--type T]` / `site get <id|slug>` / `site health <id|slug>`
- `xlyra site enable <id|slug>` / `site disable <id|slug>` / `site update <id|slug> --json '{...}'`
- `xlyra site-group list` / `site-group get <id|name>`
- `xlyra site-key list <site-id|slug>`（仅元数据，绝无明文）
- `xlyra model list [--provider P] [--category C] [--search Q] [--status S] [--page N] [--page-size N]`
- `xlyra model get <id|model_key|alias> [--matrix]`（加 `--matrix` 返回按站点模型去重后的模型↔站点矩阵与统一口径计数）
- `xlyra model create|update|delete|merge`（如 `xlyra model merge <source> <target>` 将旧/分叉模型的渠道与别名合并到目标规范模型并归档源模型）
- `xlyra model alias add <model> <alias>` / `model alias delete <model> <alias-id>`
- `xlyra model pricing set <model> [--input F] [--output F]` / `model pricing reset <model>`
- `xlyra site-model list [--site S] [--canonical M|none] [--enabled true|false] [--search Q] [--page N] [--page-size N] [--fields ...]`
- `xlyra site-model update <site> <model-id> --enabled true|false` / `site-model batch-status <site> --ids id1,id2 --enabled true|false`
- `xlyra site-model bind-canonical <site-model-id> <canonical-model-id|model_key|none>`
- `xlyra pricing list [--site S]`
- `xlyra api-key list` / `api-key get <id|name>`
- `xlyra route overview [--window-hours 24]` / `route candidates <model_key> [--limit N] [--debug]`
- `xlyra route select --model <model_key> [--api-key ID] [--debug]` / `route failover --model <model_key> --failed-site-ids id1,id2 [--debug]` / `route traces [--model <model_key>] [--limit 20]`
- `xlyra cooldown list` / `cooldown create --site <site> --duration 300 --reason "..."` / `cooldown clear --site <site>`

### 可观测性与只读诊断
- `xlyra dashboard overview` / `dashboard insights`
- `xlyra request list` / `request get <id>` / `request summary`
- `xlyra usage summary` / `usage query` / `usage timeseries`
- `xlyra plugin list` / `plugin get <id>` / `plugin-market list` / `plugin-market search <q>` / `trusted-key list`
- `xlyra audit list` / `agents-md get` / `skill list` / `skill get <name>` / `backup sections` / `cloud status`

## 退出码语义
- `0`: 成功（始终优先读取 stdout JSON 的 `.data` 与 `.diff`）
- `1`: 服务端内部错误或不可用
- `2`: 参数/协议错误（核对 `schema` 与键名/类型）
- `3`: 目标资源或路径不存在
- `4`: 策略拦截（Tier 0 禁区或 CapabilityPolicy 拒绝 —— 不要尝试绕过）
- `5`: 审批被用户或分类器拒绝，或审批超时 —— 向用户汇报结果
- `6`: 配置版本冲突（`--if-match` 过期，请重新 `config get` 获取最新 `revision`）
