package agentproxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"xlyra/server/internal/config"
)

func TestPromptSuggestionSettingsLifecycle(t *testing.T) {
	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler("", "", http.DefaultClient, slog.Default())
	requestSettings := func(body string) Settings {
		t.Helper()
		method := http.MethodGet
		reader := bytes.NewReader(nil)
		if body != "" {
			method = http.MethodPut
			reader = bytes.NewReader([]byte(body))
		}
		req := httptest.NewRequest(method, "/api/v1/agent/settings", reader)
		rec := httptest.NewRecorder()
		if method == http.MethodGet {
			h.GetSettings(rec, req, confFile)
		} else {
			h.UpdateSettings(rec, req, confFile)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", method, body, rec.Code, rec.Body.String())
		}
		var payload struct {
			Data Settings `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data
	}

	if settings := requestSettings(""); !settings.PromptSuggestionEnabled {
		t.Fatal("prompt_suggestion_enabled 默认应为 true")
	}
	if settings := requestSettings(`{"prompt_suggestion_enabled":false}`); settings.PromptSuggestionEnabled {
		t.Fatal("更新后 prompt_suggestion_enabled 应为 false")
	}
	settings := requestSettings(`{"allowed_site_ids":["site-1"],"site_policy":"allow_list"}`)
	if settings.PromptSuggestionEnabled {
		t.Fatal("部分更新不应重新开启输入预测")
	}
	if len(settings.AllowedSiteIDs) != 1 || settings.AllowedSiteIDs[0] != "site-1" || settings.SitePolicy != "allow_list" {
		t.Fatalf("部分更新未保存其他字段: %+v", settings)
	}
	if settings := requestSettings(""); settings.PromptSuggestionEnabled || PromptSuggestionEnabledFromConfig(confFile) {
		t.Fatalf("GET 未返回持久化开关: %+v", settings)
	}
	settings = requestSettings(`{"prompt_suggestion_enabled":true}`)
	if !settings.PromptSuggestionEnabled || len(settings.AllowedSiteIDs) != 1 || settings.SitePolicy != "allow_list" {
		t.Fatalf("重新开启时不应影响其他字段: %+v", settings)
	}
	if !PromptSuggestionEnabledFromConfig(nil) {
		t.Fatal("nil 配置应按默认值 true 处理")
	}
}
