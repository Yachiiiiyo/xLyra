package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"xlyra/server/internal/cloudclient"
	"xlyra/server/internal/httpx"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func (h Handler) GetCloudDeveloper(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("refresh") == "true"
	profile, err := client.Developer(r.Context(), refresh)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取开发者身份失败")
		return
	}
	httpx.JSON(w, http.StatusOK, profile)
}

func (h Handler) ListCloudSubmissions(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	pluginID := strings.TrimSpace(r.URL.Query().Get("pluginId"))
	if pluginID == "" {
		pluginID = strings.TrimSpace(r.URL.Query().Get("plugin_id"))
	}
	if pluginID == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "pluginId 必填")
		return
	}
	items, err := client.ListSubmissions(r.Context(), pluginID)
	if err != nil {
		h.writeCloudSubmissionError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h Handler) CreateCloudSubmission(client *cloudclient.Client, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "cloud_binding_unavailable", "xLyra Cloud binding is not available")
		return
	}
	if h.trafficDB == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "js plugin manager is not available")
		return
	}
	var body struct {
		PluginID    string `json:"pluginId"`
		PluginIDAlt string `json:"plugin_id"`
		Version     string `json:"version"`
		Changelog   string `json:"changelog"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	pluginID := strings.TrimSpace(body.PluginID)
	if pluginID == "" {
		pluginID = strings.TrimSpace(body.PluginIDAlt)
	}
	version := strings.TrimSpace(body.Version)
	changelog := strings.TrimSpace(body.Changelog)
	if pluginID == "" || version == "" {
		h.writeError(w, r, http.StatusBadRequest, "invalid_request", "pluginId 与 version 必填")
		return
	}
	if n := utf8.RuneCountInString(changelog); n < 1 || n > 2000 {
		h.writeError(w, r, http.StatusBadRequest, "changelog_required", cloudSubmissionMessage("changelog_required"))
		return
	}
	repo := store.NewJSPluginRepository(h.trafficDB.DB())
	row, err := repo.GetVersion(r.Context(), pluginID, version)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.writeError(w, r, http.StatusNotFound, "not_found", "插件版本不存在")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取插件版本失败")
		return
	}
	if len(row.Package) == 0 || len(row.Package) > jsplugin.MaxPackageBytes {
		h.writeError(w, r, http.StatusBadRequest, "package_unsigned", "没有可提交的已签名插件包")
		return
	}
	pkg, err := jsplugin.ReadPackage(row.Package, false)
	if err != nil || pkg.Signer == "" || pkg.Manifest.ID != pluginID || pkg.Manifest.Version != version {
		code, message := "package_unsigned", "这个版本没有签名，不能提交"
		if err != nil {
			code, message = "js_plugin_package_invalid", "插件包无法读取"
		}
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, false, code, map[string]any{
			"plugin_id": pluginID, "version": version,
		})
		h.writeError(w, r, http.StatusBadRequest, code, message)
		return
	}
	profile, err := client.Developer(r.Context(), true)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, "invalid_request", "读取开发者身份失败")
		return
	}
	if profile.Unavailable {
		h.writeCloudSubmissionError(w, r, errors.New("连接 xLyra Cloud 失败"))
		return
	}
	if profile.Status != cloudclient.DeveloperActive {
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, false, "not_developer", map[string]any{
			"plugin_id": pluginID, "version": version,
		})
		h.writeError(w, r, http.StatusConflict, "not_developer", cloudSubmissionMessage("not_developer"))
		return
	}
	if !cloudclient.OwnsPlugin(pluginID, pkg.Signer, profile) {
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, false, "plugin_not_owned", map[string]any{
			"plugin_id": pluginID, "version": version,
		})
		h.writeError(w, r, http.StatusConflict, "plugin_not_owned", "这个插件不属于当前开发者")
		return
	}
	if !versionAheadOfMarket(r.Context(), h, pluginID, version) {
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, false, "version_not_greater", map[string]any{
			"plugin_id": pluginID, "version": version,
		})
		h.writeError(w, r, http.StatusConflict, "version_not_greater", cloudSubmissionMessage("version_not_greater"))
		return
	}
	item, err := client.SubmitPackage(r.Context(), pluginID, version, row.Package, changelog)
	if err != nil {
		code := cloudSubmissionCode(err)
		h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, false, code, map[string]any{
			"plugin_id": pluginID, "version": version,
		})
		h.writeCloudSubmissionError(w, r, err)
		return
	}
	h.recordAudit(r, currentAdminActor(r), "cloud_binding.submission", "js_plugin", pluginID, true, "", map[string]any{
		"plugin_id": pluginID, "version": version, "submission_id": item.ID,
	})
	httpx.JSON(w, http.StatusOK, item)
}

func versionAheadOfMarket(ctx context.Context, h Handler, pluginID, version string) bool {
	index, ready := h.marketIndex(ctx)
	if !ready {
		return true
	}
	ahead, _ := market.Ahead(version, index, pluginID)
	return ahead
}

func (h Handler) marketIndex(ctx context.Context) (*marketproto.Index, bool) {
	if h.pluginMarket == nil {
		return nil, false
	}
	return h.pluginMarket.CachedIndex(ctx)
}

func (h Handler) cachedDeveloper(ctx context.Context) cloudclient.DeveloperProfile {
	if h.cloudClient == nil {
		return cloudclient.DeveloperProfile{Status: cloudclient.DeveloperNone}
	}
	return h.cloudClient.CachedDeveloper(ctx)
}

func (h Handler) applyDeveloperFields(item map[string]any, pluginID string, versions []store.JSPluginVersion, profile cloudclient.DeveloperProfile, index *marketproto.Index, indexReady bool) {
	item["owned"] = false
	item["market_ahead"] = false
	item["market_version"] = ""
	item["local_version"] = ""
	row, ok := market.CandidateVersion(versions)
	if ok {
		item["local_version"] = row.Version
	}
	if h.cloudClient != nil {
		if sub, cached := h.cloudClient.CachedSubmission(pluginID); cached {
			item["submission"] = sub
		}
	}
	if ok && profile.Status == cloudclient.DeveloperActive {
		item["owned"] = cloudclient.OwnsPlugin(pluginID, row.Signer, profile)
	}
	if !ok {
		return
	}
	// 还没有验签过的索引时，和市场里没有这个插件一样：本地版本可以提交。
	// 提交接口在索引未就绪时也放行，Cloud 仍会拒绝并不更大的版本。
	if !indexReady {
		item["market_ahead"] = true
		return
	}
	ahead, marketVersion := market.Ahead(row.Version, index, pluginID)
	item["market_ahead"] = ahead
	item["market_version"] = marketVersion
}

func (h Handler) writeCloudSubmissionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, cloudclient.ErrNotBound) {
		h.writeError(w, r, http.StatusConflict, "not_developer", cloudSubmissionMessage("not_developer"))
		return
	}
	var cloudErr *cloudclient.CloudError
	if errors.As(err, &cloudErr) {
		code := cloudErr.Code
		if code == "" {
			code = "cloud_error"
		}
		message := cloudSubmissionMessage(code)
		if message == "" {
			message = cloudErr.Message
		}
		if message == "" {
			message = "xLyra Cloud 请求失败"
		}
		fields := map[string]any{}
		if cloudErr.Code != "" {
			fields["cloud_code"] = cloudErr.Code
		}
		h.writeErrorFields(w, r, http.StatusFailedDependency, code, message, fields)
		return
	}
	h.writeError(w, r, http.StatusFailedDependency, "cloud_unavailable", "连接 xLyra Cloud 失败")
}

func cloudSubmissionCode(err error) string {
	var cloudErr *cloudclient.CloudError
	if errors.As(err, &cloudErr) && cloudErr.Code != "" {
		return cloudErr.Code
	}
	return "cloud_unavailable"
}

func cloudSubmissionMessage(code string) string {
	switch code {
	case "not_developer":
		return "当前账号不是已激活的开发者"
	case "no_active_key":
		return "没有可用的签名公钥，请先在开发者中心登记"
	case "namespace_not_active":
		return "插件命名空间未生效"
	case "signer_not_registered":
		return "包的签名者不在已登记的公钥中"
	case "version_not_greater":
		return "版本号必须高于市场已发布版本"
	case "too_many_pending":
		return "未结案的提交过多，请等待审核完成后再提交"
	case "daily_limit":
		return "今日提交次数已达上限"
	case "changelog_required":
		return "请填写 1–2000 字的更新日志"
	default:
		return ""
	}
}
