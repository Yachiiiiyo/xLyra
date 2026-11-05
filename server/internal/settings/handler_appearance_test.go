package settings

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func appearanceRequestWithRouteParam(req *http.Request, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("name", value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestGetAppearance_Defaults(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)
	rec := settingsPerform(h.GetAppearance, settingsTestRequest(http.MethodGet, "/settings/appearance", ""))

	settingsAssertStatus(t, rec, http.StatusOK)

	appearance := settingsDecodeJSON[AppearanceConfig](t, rec)
	if appearance.BackgroundImage != "" {
		t.Fatalf("background_image = %q, want empty", appearance.BackgroundImage)
	}
	if len(appearance.CustomBackgroundImages) != 0 {
		t.Fatalf("custom_background_images = %#v, want empty", appearance.CustomBackgroundImages)
	}
	if len(appearance.HiddenMenuItems) != 0 {
		t.Fatalf("hidden_menu_items = %#v, want empty", appearance.HiddenMenuItems)
	}
	if appearance.HomePage != "dashboard" {
		t.Fatalf("home_page = %q, want dashboard", appearance.HomePage)
	}
}

func TestUpdateAppearance_PersistsDataURLAndServesFile(t *testing.T) {
	h, confFile := settingsHandlerWithConfig(t)

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	body := `{"background_image":"` + dataURL + `","custom_background_images":["` + dataURL + `"]}`
	rec := settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", body))
	settingsAssertStatus(t, rec, http.StatusOK)

	appearance := settingsDecodeJSON[AppearanceConfig](t, rec)
	if len(appearance.CustomBackgroundImages) != 1 {
		t.Fatalf("custom_background_images = %#v, want one stored path", appearance.CustomBackgroundImages)
	}
	stored := appearance.CustomBackgroundImages[0]
	if !strings.HasPrefix(stored, appearanceBackgroundURLPrefix+appearanceFilePrefix) {
		t.Fatalf("stored path = %q, want prefix %s", stored, appearanceBackgroundURLPrefix+appearanceFilePrefix)
	}
	if appearance.BackgroundImage != stored {
		t.Fatalf("background_image = %q, want %q", appearance.BackgroundImage, stored)
	}
	if !isAppearanceBackgroundPath(stored) {
		t.Fatalf("stored path %q is not a valid background path", stored)
	}

	name := filepath.Base(stored)
	path := filepath.Join(appearanceBackgroundDir(confFile), name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("background file was not persisted: %v", err)
	}
	if string(content) != "png-bytes" {
		t.Fatalf("background file content = %q", string(content))
	}

	// GET 之后仍能读回
	rec = settingsPerform(h.GetAppearance, settingsTestRequest(http.MethodGet, "/settings/appearance", ""))
	settingsAssertStatus(t, rec, http.StatusOK)
	persisted := settingsDecodeJSON[AppearanceConfig](t, rec)
	if persisted.BackgroundImage != stored || len(persisted.CustomBackgroundImages) != 1 {
		t.Fatalf("persisted appearance = %#v", persisted)
	}

	// 文件可通过背景接口取回，带长缓存
	serveRec := httptest.NewRecorder()
	h.ServeAppearanceBackground(serveRec, appearanceRequestWithRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/appearance/backgrounds/"+name, nil), name))
	settingsAssertStatus(t, serveRec, http.StatusOK)
	if cache := serveRec.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable long cache", cache)
	}
}

func TestUpdateAppearance_RejectsInvalidInput(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)

	cases := []struct {
		name string
		body string
	}{
		{"unsupported type", `{"background_image":"","custom_background_images":["data:image/gif;base64,AA=="]}`},
		{"oversized data url", `{"background_image":"","custom_background_images":["data:image/png;base64,` + strings.Repeat("A", maxAppearanceImageBytes/3*4+4) + `"]}`},
		{"background not in gallery", `{"background_image":"/api/v1/appearance/backgrounds/appearance-bg-ffffffff.png","custom_background_images":[]}`},
		{"not a stored path", `{"background_image":"","custom_background_images":["/etc/passwd"]}`},
		{"too many images", `{"background_image":"","custom_background_images":[` + strings.Repeat(`"data:image/png;base64,AA==",`, maxAppearanceGallerySize) + `"data:image/png;base64,AA=="]}`},
		{"invalid home page", `{"home_page":"portal"}`},
		{"hidden item not a path", `{"hidden_menu_items":["dashboard"]}`},
		{"too many hidden items", `{"hidden_menu_items":[` + strings.Repeat(`"/x",`, maxHiddenMenuItems) + `"/x"]}`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			rec := settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", item.body))
			assertSettingsErrorCode(t, rec, http.StatusBadRequest, "invalid_appearance")
		})
	}
}

func TestUpdateAppearance_CleansUpRemovedGalleryFiles(t *testing.T) {
	h, confFile := settingsHandlerWithConfig(t)

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("first"))
	body := `{"background_image":"` + dataURL + `","custom_background_images":["` + dataURL + `"]}`
	rec := settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", body))
	settingsAssertStatus(t, rec, http.StatusOK)
	first := settingsDecodeJSON[AppearanceConfig](t, rec).BackgroundImage
	firstPath := filepath.Join(appearanceBackgroundDir(confFile), filepath.Base(first))
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("first background file missing: %v", err)
	}

	// 删除图库项并回到星点背景：不再被引用的文件应被清理
	rec = settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", `{"background_image":"","custom_background_images":[]}`))
	settingsAssertStatus(t, rec, http.StatusOK)
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("removed background file still exists (err=%v)", err)
	}
}

func TestServeAppearanceBackground_RejectsTraversal(t *testing.T) {
	h, confFile := settingsHandlerWithConfig(t)

	for _, name := range []string{"../config.json", "appearance-bg-..%2fconfig.json", "agent-bg-x.png", "", "."} {
		t.Run(name, func(t *testing.T) {
			req := appearanceRequestWithRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/appearance/backgrounds/"+name, nil), name)
			rec := settingsPerform(h.ServeAppearanceBackground, req)
			settingsAssertStatus(t, rec, http.StatusNotFound)
		})
	}

	// 合法前缀但不存在的文件同样是 404
	req := appearanceRequestWithRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/appearance/backgrounds/appearance-bg-deadbeef.png", nil), "appearance-bg-deadbeef.png")
	rec := settingsPerform(h.ServeAppearanceBackground, req)
	settingsAssertStatus(t, rec, http.StatusNotFound)

	// 未配置 config 时不可用
	rec = settingsPerform(settingsHandlerWithoutConfig().ServeAppearanceBackground, appearanceRequestWithRouteParam(httptest.NewRequest(http.MethodGet, "/api/v1/appearance/backgrounds/appearance-bg-deadbeef.png", nil), "appearance-bg-deadbeef.png"))
	settingsAssertStatus(t, rec, http.StatusNotFound)
	_ = confFile
}

func TestUpdateAppearance_WithoutConfigIsUnavailable(t *testing.T) {
	rec := settingsPerform(settingsHandlerWithoutConfig().UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", `{}`))
	assertSettingsErrorCode(t, rec, http.StatusServiceUnavailable, "config_unavailable")
}

func TestUpdateAppearance_MenuSettingsRoundTrip(t *testing.T) {
	h, _ := settingsHandlerWithConfig(t)

	body := `{"background_image":"","custom_background_images":[],"hidden_menu_items":["/dashboard","/dashboard","/sites"],"home_page":"agent"}`
	rec := settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", body))
	settingsAssertStatus(t, rec, http.StatusOK)

	appearance := settingsDecodeJSON[AppearanceConfig](t, rec)
	if len(appearance.HiddenMenuItems) != 2 || appearance.HiddenMenuItems[0] != "/dashboard" || appearance.HiddenMenuItems[1] != "/sites" {
		t.Fatalf("hidden_menu_items = %#v, want deduplicated [/dashboard /sites]", appearance.HiddenMenuItems)
	}
	if appearance.HomePage != "agent" {
		t.Fatalf("home_page = %q, want agent", appearance.HomePage)
	}

	rec = settingsPerform(h.GetAppearance, settingsTestRequest(http.MethodGet, "/settings/appearance", ""))
	settingsAssertStatus(t, rec, http.StatusOK)
	persisted := settingsDecodeJSON[AppearanceConfig](t, rec)
	if len(persisted.HiddenMenuItems) != 2 || persisted.HomePage != "agent" {
		t.Fatalf("persisted menu settings = %#v", persisted)
	}

	// 旧配置兼容：空串 home_page 归一为 dashboard，nil hidden 为空切片
	rec = settingsPerform(h.UpdateAppearance, settingsRawJSONRequest(http.MethodPut, "/settings/appearance", `{"background_image":"","custom_background_images":[]}`))
	settingsAssertStatus(t, rec, http.StatusOK)
	fallback := settingsDecodeJSON[AppearanceConfig](t, rec)
	if fallback.HomePage != "dashboard" {
		t.Fatalf("home_page = %q, want dashboard", fallback.HomePage)
	}
	if fallback.HiddenMenuItems == nil || len(fallback.HiddenMenuItems) != 0 {
		t.Fatalf("hidden_menu_items = %#v, want empty non-nil", fallback.HiddenMenuItems)
	}
}
