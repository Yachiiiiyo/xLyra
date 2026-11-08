package agentproxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"xlyra/server/internal/config"
)

type fastModelSettingsResponse struct {
	Data  Settings `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func requestFastModelSettings(t *testing.T, h *Handler, confFile *config.ConfigFile, body string, wantStatus int) fastModelSettingsResponse {
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
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status=%d body=%s", method, body, rec.Code, rec.Body.String())
	}
	var payload fastModelSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestFastModelSettingsLifecycle(t *testing.T) {
	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(slog.Default())

	if settings := requestFastModelSettings(t, h, confFile, "", http.StatusOK).Data; settings.FastModel != "" {
		t.Fatalf("fast_model 默认应为空串: %+v", settings)
	}
	if got := FastModelFromConfig(nil); got != "" {
		t.Fatalf("nil 配置 fast model = %q", got)
	}

	settings := requestFastModelSettings(t, h, confFile, `{"fast_model":" fast-test ","prompt_suggestion_enabled":false,"image_models":["gpt-image-1"]}`, http.StatusOK).Data
	wantImages := []ImageModelSelection{{Model: "gpt-image-1", SiteIDs: []string{}}}
	if settings.FastModel != "fast-test" || settings.PromptSuggestionEnabled || !reflect.DeepEqual(settings.ImageModels, wantImages) {
		t.Fatalf("设置 fast_model 响应错误: %+v", settings)
	}
	settings = requestFastModelSettings(t, h, confFile, `{"fast_model":"fast-next"}`, http.StatusOK).Data
	if settings.FastModel != "fast-next" || settings.PromptSuggestionEnabled || !reflect.DeepEqual(settings.ImageModels, wantImages) {
		t.Fatalf("fast_model 部分更新影响了其他字段: %+v", settings)
	}
	settings = requestFastModelSettings(t, h, confFile, `{"allowed_site_ids":["site-1"]}`, http.StatusOK).Data
	if settings.FastModel != "fast-next" || settings.PromptSuggestionEnabled || !reflect.DeepEqual(settings.ImageModels, wantImages) {
		t.Fatalf("其他字段部分更新影响了 fast_model: %+v", settings)
	}

	settings = requestFastModelSettings(t, h, confFile, `{"fast_model":"  "}`, http.StatusOK).Data
	if settings.FastModel != "" || FastModelFromConfig(confFile) != "" {
		t.Fatalf("空白 fast_model 应清空配置: %+v", settings)
	}
	if settings.PromptSuggestionEnabled || !reflect.DeepEqual(settings.ImageModels, wantImages) {
		t.Fatalf("清空 fast_model 影响了其他字段: %+v", settings)
	}

	settings = requestFastModelSettings(t, h, confFile, `{"fast_model":"preserved"}`, http.StatusOK).Data
	if settings.FastModel != "preserved" {
		t.Fatalf("fast_model = %q, want preserved", settings.FastModel)
	}
	payload := requestFastModelSettings(t, h, confFile, `{"fast_model":"`+strings.Repeat("模", 201)+`"}`, http.StatusBadRequest)
	if payload.Error.Code != "invalid_fast_model" {
		t.Fatalf("超长 fast_model 错误码 = %q", payload.Error.Code)
	}
	settings = requestFastModelSettings(t, h, confFile, "", http.StatusOK).Data
	if settings.FastModel != "preserved" || settings.PromptSuggestionEnabled || !reflect.DeepEqual(settings.ImageModels, wantImages) {
		t.Fatalf("超长 fast_model 不应修改配置: %+v", settings)
	}
}
