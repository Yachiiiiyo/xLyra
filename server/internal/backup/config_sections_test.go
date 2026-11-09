package backup

import (
	"reflect"
	"testing"
)

// backupSectionsConfigFixture 构造一份覆盖各类别配置键的原始配置。
func backupSectionsConfigFixture() map[string]any {
	return map[string]any{
		"global": map[string]any{"app_name": "xlyra"},
		"network": map[string]any{
			"listen": ":8080",
			"proxy":  map[string]any{"url": "http://proxy"},
		},
		"agent": map[string]any{
			"enabled":               true,
			"runner_base_url":       "https://runner.example.com",
			"runner_internal_token": "secret-token",
			"model":                 "test-model",
		},
		"backup": map[string]any{
			"automatic": map[string]any{
				"enabled":         true,
				"cron":            "0 3 * * *",
				"retention_count": 7,
				"storage": map[string]any{
					"endpoint":                    "http://minio",
					"bucket":                      "backups",
					"prefix":                      "xlyra/",
					"access_key":                  "ak",
					"secret_key_encrypted":        "sk-encrypted",
					"secret_key_masked":           "sk-masked",
					"force_path_style":            true,
					"use_ssl":                     false,
					"backup_passphrase_encrypted": "pp-encrypted",
					"backup_passphrase_masked":    "pp-masked",
				},
			},
		},
	}
}

func TestConfigKeyOwner(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"network":    sectionGatewaySites,
		"agent":      sectionAgentConfig,
		"global":     sectionSettings,
		"backup":     sectionSettings,
		"appearance": sectionSettings,
		"unknown":    sectionSettings,
	}
	for key, want := range cases {
		if got := configKeyOwner(key); got != want {
			t.Fatalf("configKeyOwner(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestFilterConfigForSections(t *testing.T) {
	t.Parallel()

	t.Run("只选 settings 时保留全部未认领键并去掉自动备份密钥", func(t *testing.T) {
		t.Parallel()

		got := filterConfigForSections(backupSectionsConfigFixture(), []string{sectionSettings})
		if _, ok := got["network"]; ok {
			t.Fatalf("network should not be exported for settings only: %#v", got)
		}
		if _, ok := got["agent"]; ok {
			t.Fatalf("agent should not be exported for settings only: %#v", got)
		}
		storage := got["backup"].(map[string]any)["automatic"].(map[string]any)["storage"].(map[string]any)
		for _, key := range []string{"secret_key_encrypted", "secret_key_masked", "backup_passphrase_encrypted", "backup_passphrase_masked"} {
			if _, ok := storage[key]; ok {
				t.Fatalf("automatic backup storage credential %q should be stripped", key)
			}
		}
		if storage["endpoint"] != "http://minio" {
			t.Fatalf("automatic backup storage endpoint = %#v", storage["endpoint"])
		}
	})

	t.Run("只选 gateway_sites 时只带 network", func(t *testing.T) {
		t.Parallel()

		got := filterConfigForSections(backupSectionsConfigFixture(), []string{sectionGatewaySites})
		if len(got) != 1 {
			t.Fatalf("filtered config = %#v, want only network", got)
		}
		if !reflect.DeepEqual(got["network"], backupSectionsConfigFixture()["network"]) {
			t.Fatalf("network = %#v", got["network"])
		}
	})

	t.Run("只选 agent_config 时不带 runner_*", func(t *testing.T) {
		t.Parallel()

		got := filterConfigForSections(backupSectionsConfigFixture(), []string{sectionAgentConfig})
		if len(got) != 1 {
			t.Fatalf("filtered config = %#v, want only agent", got)
		}
		agent := got["agent"].(map[string]any)
		for _, key := range agentRunnerLegacyKeys {
			if _, ok := agent[key]; ok {
				t.Fatalf("agent.%s should be stripped", key)
			}
		}
		if agent["model"] != "test-model" {
			t.Fatalf("agent.model = %#v", agent["model"])
		}
	})

	t.Run("全选时等于旧的全量去掉 runner_*", func(t *testing.T) {
		t.Parallel()

		data := backupSectionsConfigFixture()
		got := filterConfigForSections(data, v4SectionsWithoutAgent())
		want := backupSectionsConfigFixture()
		delete(want["agent"].(map[string]any), "runner_base_url")
		delete(want["agent"].(map[string]any), "runner_internal_token")
		// settings 被选中,自动备份的存储密钥被去掉。
		wantStorage := want["backup"].(map[string]any)["automatic"].(map[string]any)["storage"].(map[string]any)
		delete(wantStorage, "secret_key_encrypted")
		delete(wantStorage, "secret_key_masked")
		delete(wantStorage, "backup_passphrase_encrypted")
		delete(wantStorage, "backup_passphrase_masked")
		// SanitizeConfigForBackup 会补齐规范化字段。
		wantStorage["region"] = ""
		wantStorage["skip_tls_verify"] = false
		// sections 为 nil 时导出写空数组。
		want["backup"].(map[string]any)["automatic"].(map[string]any)["sections"] = []string{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("filtered config = %#v, want %#v", got, want)
		}
	})

	t.Run("过滤结果不影响原始数据", func(t *testing.T) {
		t.Parallel()

		data := backupSectionsConfigFixture()
		_ = filterConfigForSections(data, []string{sectionAgentConfig})
		agent := data["agent"].(map[string]any)
		if _, ok := agent["runner_base_url"]; !ok {
			t.Fatalf("source agent config should be untouched: %#v", agent)
		}
	})
}

func TestMergeConfigForRestore(t *testing.T) {
	t.Parallel()

	t.Run("只恢复 agent_config 时其它键保持 current", func(t *testing.T) {
		t.Parallel()

		current := backupSectionsConfigFixture()
		imported := map[string]any{
			"agent":   map[string]any{"model": "imported-model"},
			"network": map[string]any{"listen": ":9999"},
		}
		got := mergeConfigForRestore(current, imported, []string{sectionAgentConfig})
		if got["agent"].(map[string]any)["model"] != "imported-model" {
			t.Fatalf("agent.model = %#v, want imported", got["agent"])
		}
		if !reflect.DeepEqual(got["network"], current["network"]) {
			t.Fatalf("network = %#v, want current", got["network"])
		}
		if !reflect.DeepEqual(got["backup"], current["backup"]) {
			t.Fatalf("backup = %#v, want current", got["backup"])
		}
	})

	t.Run("恢复 settings 时 backup.automatic 的存储凭据保留 current", func(t *testing.T) {
		t.Parallel()

		current := backupSectionsConfigFixture()
		imported := map[string]any{
			"backup": map[string]any{
				"automatic": map[string]any{
					"enabled":         false,
					"cron":            "30 4 * * *",
					"retention_count": 3,
					"storage": map[string]any{
						"endpoint":         "http://other",
						"bucket":           "other-bucket",
						"access_key":       "other-ak",
						"force_path_style": true,
						"use_ssl":          true,
					},
				},
			},
		}
		got := mergeConfigForRestore(current, imported, []string{sectionSettings})
		automatic := got["backup"].(map[string]any)["automatic"].(map[string]any)
		// 当前配置带有存储凭据时,MergeImportedConfig 用当前的 automatic 整体
		// 替换导入值,存储凭据得以保留。
		storage := automatic["storage"].(map[string]any)
		if storage["secret_key_encrypted"] != "sk-encrypted" || storage["backup_passphrase_encrypted"] != "pp-encrypted" {
			t.Fatalf("storage credentials = %#v, want current", storage)
		}
		if automatic["enabled"] != true {
			t.Fatalf("automatic = %#v, want current value when storage is configured", automatic)
		}
	})

	t.Run("恢复 agent_config 时 runner_* 保留 current", func(t *testing.T) {
		t.Parallel()

		current := backupSectionsConfigFixture()
		imported := map[string]any{
			"agent": map[string]any{
				"model":                 "imported-model",
				"runner_base_url":       "https://imported-runner",
				"runner_internal_token": "imported-token",
			},
		}
		got := mergeConfigForRestore(current, imported, []string{sectionAgentConfig})
		agent := got["agent"].(map[string]any)
		if agent["model"] != "imported-model" {
			t.Fatalf("agent.model = %#v, want imported", agent)
		}
		if agent["runner_base_url"] != "https://runner.example.com" {
			t.Fatalf("agent.runner_base_url = %#v, want current", agent["runner_base_url"])
		}
		if agent["runner_internal_token"] != "secret-token" {
			t.Fatalf("agent.runner_internal_token = %#v, want current", agent["runner_internal_token"])
		}
	})

	t.Run("恢复的类别里 imported 缺键时删除对应键", func(t *testing.T) {
		t.Parallel()

		current := backupSectionsConfigFixture()
		imported := map[string]any{"network": map[string]any{"listen": ":9999"}}
		got := mergeConfigForRestore(current, imported, []string{sectionSettings, sectionGatewaySites})
		if _, ok := got["global"]; ok {
			t.Fatalf("global should be deleted when imported lacks it: %#v", got)
		}
		if !reflect.DeepEqual(got["network"], imported["network"]) {
			t.Fatalf("network = %#v, want imported", got["network"])
		}
		// settings 被恢复时 backup 键由 MergeImportedConfig 重建,
		// 保留当前的自动备份配置与存储凭据。
		automatic, ok := got["backup"].(map[string]any)["automatic"].(map[string]any)
		if !ok {
			t.Fatalf("backup.automatic should be preserved: %#v", got["backup"])
		}
		if _, ok := automatic["storage"].(map[string]any)["secret_key_encrypted"]; !ok {
			t.Fatalf("backup.automatic should preserve current storage credentials: %#v", automatic)
		}
	})
}
