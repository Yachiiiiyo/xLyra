package agentproxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"xlyra/server/internal/config"
)

func TestImageModelsSettingsLifecycle(t *testing.T) {
	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(slog.Default())
	assertSettings := func(body string, wantStatus int) Settings {
		t.Helper()
		method := http.MethodGet
		var reader *bytes.Reader
		if body != "" {
			method = http.MethodPut
			reader = bytes.NewReader([]byte(body))
		} else {
			reader = bytes.NewReader(nil)
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
		var payload struct {
			Data Settings `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data
	}

	siteA := "11111111-1111-4111-8111-111111111111"
	siteB := "22222222-2222-4222-8222-222222222222"
	if got := assertSettings("", http.StatusOK).ImageModels; len(got) != 0 || got == nil {
		t.Fatalf("default image_models = %#v, want []", got)
	}
	settings := assertSettings(`{"allowed_site_ids":["site-1"],"image_models":[{"model":" gpt-image-2 ","site_ids":["`+siteA+`","not-a-uuid","`+siteA+`"]},{"model":"dall-e-3","site_ids":[]},{"model":"gpt-image-2","site_ids":["`+siteB+`","`+siteA+`"]},{"model":""}]}`, http.StatusOK)
	want := []ImageModelSelection{{Model: "gpt-image-2", SiteIDs: []string{siteA, siteB}}, {Model: "dall-e-3", SiteIDs: []string{}}}
	if !reflect.DeepEqual(settings.ImageModels, want) {
		t.Fatalf("image_models = %#v, want %#v", settings.ImageModels, want)
	}
	settings = assertSettings(`{"image_models":[{"model":"other-image","site_ids":["`+siteA+`"]}]}`, http.StatusOK)
	if !reflect.DeepEqual(settings.AllowedSiteIDs, []string{"site-1"}) {
		t.Fatalf("image-only update changed site allowlist: %#v", settings.AllowedSiteIDs)
	}
	settings = assertSettings(`{"allowed_site_ids":["site-2"]}`, http.StatusOK)
	if want := []ImageModelSelection{{Model: "other-image", SiteIDs: []string{siteA}}}; !reflect.DeepEqual(settings.ImageModels, want) {
		t.Fatalf("scope-only update changed image models: %#v", settings.ImageModels)
	}
	if got := assertSettings(`{"image_models":[]}`, http.StatusOK).ImageModels; len(got) != 0 || got == nil {
		t.Fatalf("cleared image_models = %#v", got)
	}

	assertSettings(`{"image_models":[{"model":"`+strings.Repeat("模", 201)+`"}]}`, http.StatusBadRequest)
	assertSettings(`{"image_models":[1]}`, http.StatusBadRequest)

	// 旧字符串元素表示不限站点；与站点选择合并时仍保持不限站点。
	settings = assertSettings(`{"image_models":[{"model":"gpt-image-2","site_ids":["`+siteA+`"]},"gpt-image-2"," dall-e-3 "]}`, http.StatusOK)
	want = []ImageModelSelection{{Model: "gpt-image-2", SiteIDs: []string{}}, {Model: "dall-e-3", SiteIDs: []string{}}}
	if !reflect.DeepEqual(settings.ImageModels, want) {
		t.Fatalf("legacy image_models = %#v, want %#v", settings.ImageModels, want)
	}

	settings = assertSettings(`{"image_models":[{"model":"gpt-image-2","site_ids":["`+siteA+`"]},{"model":"dall-e-3","site_ids":[]}]}`, http.StatusOK)
	reloaded, err := config.LoadConfigFile(filepath.Dir(filepath.Dir(confFile.Path())))
	if err != nil {
		t.Fatal(err)
	}
	want = []ImageModelSelection{{Model: "gpt-image-2", SiteIDs: []string{siteA}}, {Model: "dall-e-3", SiteIDs: []string{}}}
	if got := ImageModelsFromConfig(reloaded); !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded image models = %#v, want %#v", got, want)
	}

	// 直接读取旧版字符串数组时也转换为对象，而不是丢失配置。
	if err := reloaded.Set("agent.image_models", []string{"gpt-image-1", "gpt-image-1"}); err != nil {
		t.Fatal(err)
	}
	if got := ImageModelsFromConfig(reloaded); !reflect.DeepEqual(got, []ImageModelSelection{{Model: "gpt-image-1", SiteIDs: []string{}}}) {
		t.Fatalf("legacy persisted image models = %#v", got)
	}
	if got := ImageModelsFromConfig(nil); len(got) != 0 || got == nil {
		t.Fatalf("nil config image models = %#v", got)
	}
}
