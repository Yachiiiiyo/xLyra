package backup

import (
	"slices"

	"xlyra/server/internal/config"
)

// agentRunnerLegacyKeys 是旧 Agent 方案遗留在 agent 子树里的连接配置,
// 不再随备份导出,恢复时也忽略导入值、保留当前值。
var agentRunnerLegacyKeys = []string{"runner_base_url", "runner_internal_token"}

// configKeyOwner 返回配置顶层键所属的类别:某类别的 ConfigPaths 认领了
// 该键就属于它,未被认领的键一律属于 settings。
func configKeyOwner(key string) string {
	for _, section := range sections {
		if slices.Contains(section.ConfigPaths, key) {
			return section.ID
		}
	}
	return sectionSettings
}

// filterConfigForSections 深拷贝出所选类别拥有的配置顶层键。选了 settings 时
// 先对整份数据做 SanitizeConfigForBackup(去掉自动备份的存储密钥)再过滤;
// 选了 agent_config 时从 agent 子树删除旧 Agent 方案遗留的 runner_* 连接配置。
func filterConfigForSections(data map[string]any, ids []string) map[string]any {
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	if _, ok := selected[sectionSettings]; ok {
		data = config.SanitizeConfigForBackup(data)
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		if _, ok := selected[configKeyOwner(key)]; !ok {
			continue
		}
		out[key] = deepCopyValue(value)
	}
	if _, ok := selected[sectionAgentConfig]; ok {
		stripAgentRunnerLegacyKeys(out)
	}
	return out
}

// stripAgentRunnerLegacyKeys 从 agent 子树删除 runner_* 连接配置。
func stripAgentRunnerLegacyKeys(data map[string]any) {
	agent, ok := data["agent"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range agentRunnerLegacyKeys {
		delete(agent, key)
	}
}

// mergeConfigForRestore 按恢复的类别合并导入配置:以 current 的深拷贝为基础,
// 对 current ∪ imported 的每个顶层键,若其所属类别在 restored 里则取 imported
// 的值(imported 里没有则删除该键),否则保持 current。选了 settings 时 backup
// 键的值取 MergeImportedConfig 的结果(保留当前自动备份的存储凭据);选了
// agent_config 时导入的 agent 子树忽略 runner_* 连接配置,保留 current 现值。
func mergeConfigForRestore(current map[string]any, imported map[string]any, restored []string) map[string]any {
	restoredSet := make(map[string]struct{}, len(restored))
	for _, id := range restored {
		restoredSet[id] = struct{}{}
	}
	out := make(map[string]any, len(current))
	for key, value := range current {
		out[key] = deepCopyValue(value)
	}
	for _, key := range unionTopLevelKeys(current, imported) {
		if _, ok := restoredSet[configKeyOwner(key)]; !ok {
			continue
		}
		value, ok := imported[key]
		if !ok {
			delete(out, key)
			continue
		}
		out[key] = deepCopyValue(value)
	}
	if _, ok := restoredSet[sectionSettings]; ok {
		if backupValue, ok := config.MergeImportedConfig(current, imported)["backup"]; ok {
			out["backup"] = backupValue
		}
	}
	if _, ok := restoredSet[sectionAgentConfig]; ok {
		mergeAgentRunnerLegacyKeys(current, out)
	}
	return out
}

// mergeAgentRunnerLegacyKeys 让 out 的 agent 子树保留 current 里 runner_*
// 连接配置的现值;current 没有则删除。
func mergeAgentRunnerLegacyKeys(current map[string]any, out map[string]any) {
	currentAgent, _ := current["agent"].(map[string]any)
	outAgent, ok := out["agent"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range agentRunnerLegacyKeys {
		if value, ok := currentAgent[key]; ok {
			outAgent[key] = deepCopyValue(value)
		} else {
			delete(outAgent, key)
		}
	}
}

// unionTopLevelKeys 返回两份配置的全部顶层键,按 current 的键序在前。
func unionTopLevelKeys(current map[string]any, imported map[string]any) []string {
	seen := make(map[string]struct{}, len(current)+len(imported))
	out := make([]string, 0, len(current)+len(imported))
	for _, data := range []map[string]any{current, imported} {
		for key := range data {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	return out
}

// deepCopyValue 深拷贝 JSON 风格的配置值(map/slice 递归,其余按值复制)。
func deepCopyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = deepCopyValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}
