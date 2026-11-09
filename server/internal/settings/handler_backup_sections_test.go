package settings

import (
	"net/http"
	"testing"

	"xlyra/server/internal/backup"
)

func TestGetBackupSectionsReturnsCatalogAndDefaults(t *testing.T) {
	t.Parallel()

	confFile := settingsConfigFile(t)
	h := Handler{backups: backup.NewService(nil, confFile, settingsTestMasterKey)}

	req := settingsTestRequest(http.MethodGet, "/settings/backup/sections", "")
	rec := settingsPerform(h.GetBackupSections, req)

	settingsAssertStatus(t, rec, http.StatusOK)
	body := settingsDecodeJSON[struct {
		Sections []backup.SectionInfo `json:"sections"`
		Defaults struct {
			Manual []string `json:"manual"`
			Auto   []string `json:"auto"`
		} `json:"defaults"`
	}](t, rec)

	if len(body.Sections) != len(backup.Sections()) {
		t.Fatalf("sections count = %d, want %d", len(body.Sections), len(backup.Sections()))
	}
	for i, section := range body.Sections {
		if section.ID != backup.Sections()[i].ID {
			t.Fatalf("sections[%d].ID = %q, want registry order", i, section.ID)
		}
	}
	// 未配置 agent 目录时,默认类别里不应出现 agent 相关类别。
	for _, id := range body.Defaults.Manual {
		if id == "agent_capabilities" || id == "agent_sessions" || id == "agent_workspace" {
			t.Fatalf("manual defaults contain unavailable section %q", id)
		}
	}
	for _, id := range body.Defaults.Auto {
		if id == "agent_capabilities" || id == "agent_sessions" || id == "agent_workspace" {
			t.Fatalf("auto defaults contain unavailable section %q", id)
		}
	}
	if len(body.Defaults.Manual) == 0 || len(body.Defaults.Auto) == 0 {
		t.Fatalf("defaults should not be empty: %#v", body.Defaults)
	}
}

func TestGetBackupSectionsMarksAgentSectionsConfigured(t *testing.T) {
	t.Parallel()

	confFile := settingsConfigFile(t)
	h := Handler{backups: backup.NewService(nil, confFile, settingsTestMasterKey).WithAgentRoot(t.TempDir())}

	req := settingsTestRequest(http.MethodGet, "/settings/backup/sections", "")
	rec := settingsPerform(h.GetBackupSections, req)

	settingsAssertStatus(t, rec, http.StatusOK)
	body := settingsDecodeJSON[struct {
		Sections []backup.SectionInfo `json:"sections"`
		Defaults struct {
			Manual []string `json:"manual"`
			Auto   []string `json:"auto"`
		} `json:"defaults"`
	}](t, rec)

	found := false
	for _, section := range body.Sections {
		if section.ID != "agent_capabilities" {
			continue
		}
		found = true
		if !section.Available {
			t.Fatalf("agent_capabilities should be available with agent root: %#v", section)
		}
	}
	if !found {
		t.Fatal("agent_capabilities missing from catalog")
	}
	contains := func(ids []string, id string) bool {
		for _, current := range ids {
			if current == id {
				return true
			}
		}
		return false
	}
	if !contains(body.Defaults.Manual, "agent_capabilities") || !contains(body.Defaults.Auto, "agent_capabilities") {
		t.Fatalf("agent_capabilities should be in defaults: %#v", body.Defaults)
	}
}
