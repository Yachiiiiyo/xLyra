package agentproxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xlyra/server/internal/config"
)

type classifierModelSettingsResponse struct {
	Data  Settings `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func requestClassifierModelSettings(t *testing.T, h *Handler, confFile *config.ConfigFile, body string, wantStatus int) classifierModelSettingsResponse {
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
	var payload classifierModelSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestClassifierModelSettingsLifecycle(t *testing.T) {
	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(slog.Default())

	if settings := requestClassifierModelSettings(t, h, confFile, "", http.StatusOK).Data; settings.ClassifierModel != "" {
		t.Fatalf("classifier_model 默认应为空串: %+v", settings)
	}
	if got := ClassifierModelFromConfig(nil); got != "" {
		t.Fatalf("nil 配置 classifier model = %q", got)
	}

	settings := requestClassifierModelSettings(t, h, confFile, `{"classifier_model":" classifier-test ","fast_model":"fast-test"}`, http.StatusOK).Data
	if settings.ClassifierModel != "classifier-test" || settings.FastModel != "fast-test" {
		t.Fatalf("设置 classifier_model 响应错误: %+v", settings)
	}
	settings = requestClassifierModelSettings(t, h, confFile, `{"classifier_model":"classifier-next"}`, http.StatusOK).Data
	if settings.ClassifierModel != "classifier-next" || settings.FastModel != "fast-test" {
		t.Fatalf("classifier_model 部分更新影响了 fast_model: %+v", settings)
	}
	settings = requestClassifierModelSettings(t, h, confFile, `{"fast_model":"fast-next"}`, http.StatusOK).Data
	if settings.ClassifierModel != "classifier-next" || settings.FastModel != "fast-next" {
		t.Fatalf("fast_model 部分更新影响了 classifier_model: %+v", settings)
	}

	settings = requestClassifierModelSettings(t, h, confFile, `{"classifier_model":"  "}`, http.StatusOK).Data
	if settings.ClassifierModel != "" || ClassifierModelFromConfig(confFile) != "" {
		t.Fatalf("空白 classifier_model 应清空配置: %+v", settings)
	}
	if settings.FastModel != "fast-next" {
		t.Fatalf("清空 classifier_model 影响了 fast_model: %+v", settings)
	}

	settings = requestClassifierModelSettings(t, h, confFile, `{"classifier_model":"preserved"}`, http.StatusOK).Data
	if settings.ClassifierModel != "preserved" {
		t.Fatalf("classifier_model = %q, want preserved", settings.ClassifierModel)
	}
	payload := requestClassifierModelSettings(t, h, confFile, `{"classifier_model":"`+strings.Repeat("模", 201)+`"}`, http.StatusBadRequest)
	if payload.Error.Code != "invalid_classifier_model" {
		t.Fatalf("超长 classifier_model 错误码 = %q", payload.Error.Code)
	}
	settings = requestClassifierModelSettings(t, h, confFile, "", http.StatusOK).Data
	if settings.ClassifierModel != "preserved" || settings.FastModel != "fast-next" {
		t.Fatalf("超长 classifier_model 不应修改配置: %+v", settings)
	}
}
