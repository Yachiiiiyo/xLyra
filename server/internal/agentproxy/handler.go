package agentproxy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpx"
)

type Handler struct {
	mu                    sync.RWMutex
	logger                *slog.Logger
	workdir               string
	backgroundDirFallback string

	onSettingsUpdated func()
}

type ImageModelSelection struct {
	Model   string   `json:"model"`
	SiteIDs []string `json:"site_ids"`
}

// UnmarshalJSON 同时接受旧版模型名字符串和新版模型、站点选择对象。
func (s *ImageModelSelection) UnmarshalJSON(data []byte) error {
	var model string
	if err := json.Unmarshal(data, &model); err == nil {
		s.Model = model
		s.SiteIDs = []string{}
		return nil
	}
	type selection ImageModelSelection
	var value selection
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = ImageModelSelection(value)
	if s.SiteIDs == nil {
		s.SiteIDs = []string{}
	}
	return nil
}

type Settings struct {
	AllowedSiteIDs          []string              `json:"allowed_site_ids"`
	AllowedSiteModelIDs     []string              `json:"allowed_site_model_ids"`
	ImageModels             []ImageModelSelection `json:"image_models"`
	PromptSuggestionEnabled bool                  `json:"prompt_suggestion_enabled"`
	FastModel               string                `json:"fast_model"`
	ClassifierModel         string                `json:"classifier_model"`
	SitePolicy              string                `json:"site_policy"`
	ModelPolicy             string                `json:"model_policy"`
	Appearance              AppearanceSettings    `json:"appearance"`
}

type AppearanceSettings struct {
	BackgroundImage        string   `json:"background_image"`
	CustomBackgroundImages []string `json:"custom_background_images"`
	SideTransparency       int      `json:"side_transparency"`
	SideBrightness         int      `json:"side_brightness"`
	SideThickness          int      `json:"side_thickness"`
	BackdropBlur           int      `json:"backdrop_blur"`
	BackdropDim            int      `json:"backdrop_dim"`
}

const (
	defaultBackgroundImage  = "/agent-backdrop.png"
	defaultSideTransparency = 49
	defaultSideBrightness   = 32
	defaultSideThickness    = 28
	defaultBackdropBlur     = 13
	defaultBackdropDim      = 69
	maxBackgroundImageBytes = 8 * 1024 * 1024
	maxAgentAttachments     = 4
	maxAgentAttachmentBytes = 5 * 1024 * 1024
	maxAgentAttachmentTotal = 20 * 1024 * 1024
)

func defaultAppearanceSettings() AppearanceSettings {
	return AppearanceSettings{
		BackgroundImage:        defaultBackgroundImage,
		CustomBackgroundImages: []string{},
		SideTransparency:       defaultSideTransparency,
		SideBrightness:         defaultSideBrightness,
		SideThickness:          defaultSideThickness,
		BackdropBlur:           defaultBackdropBlur,
		BackdropDim:            defaultBackdropDim,
	}
}

// settingsInput uses partial-update semantics: absent fields are left unchanged,
// so site/model scope can be saved independently.
type settingsInput struct {
	ClearRunner             bool                   `json:"clear_runner"`
	AllowedSiteIDs          *[]string              `json:"allowed_site_ids"`
	AllowedSiteModelIDs     *[]string              `json:"allowed_site_model_ids"`
	ImageModels             *[]ImageModelSelection `json:"image_models"`
	PromptSuggestionEnabled *bool                  `json:"prompt_suggestion_enabled"`
	FastModel               *string                `json:"fast_model"`
	ClassifierModel         *string                `json:"classifier_model"`
	SitePolicy              *string                `json:"site_policy"`
	ModelPolicy             *string                `json:"model_policy"`
	Appearance              *AppearanceSettings    `json:"appearance"`
}

func NewHandler(logger *slog.Logger, workdirs ...string) *Handler {
	workdir := ""
	if len(workdirs) > 0 {
		workdir = strings.TrimSpace(workdirs[0])
	}
	return &Handler{logger: logger, workdir: workdir}
}

func validateAgentAttachments(value any) error {
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return errors.New("attachments must be an array")
	}
	if len(items) > maxAgentAttachments {
		return fmt.Errorf("attachments cannot contain more than %d files", maxAgentAttachments)
	}
	total := 0
	for _, item := range items {
		attachment, ok := item.(map[string]any)
		if !ok {
			return errors.New("attachment must be an object")
		}
		name, _ := attachment["name"].(string)
		mime, _ := attachment["mime_type"].(string)
		dataURL, _ := attachment["data_url"].(string)
		if strings.TrimSpace(name) == "" || strings.TrimSpace(mime) == "" || dataURL == "" {
			return errors.New("attachment name, mime_type and data_url are required")
		}
		parts := strings.SplitN(dataURL, ",", 2)
		if len(parts) != 2 || !strings.Contains(strings.ToLower(parts[0]), ";base64") {
			return errors.New("attachment data_url must be a base64 data URL")
		}
		// 大小按 base64 串长度算术推算，不做全量解码（同 isDataURLTooLarge）；
		// 解码留给实际消费方（agent/上游），代理层只负责限额
		size, ok := decodedBase64Size(parts[1])
		if !ok {
			return errors.New("attachment data_url is not valid base64")
		}
		if size > maxAgentAttachmentBytes {
			return fmt.Errorf("attachment %q exceeds the 5 MB limit", name)
		}
		total += size
		if total > maxAgentAttachmentTotal {
			return errors.New("total attachment size exceeds the 20 MB limit")
		}
	}
	return nil
}

// ValidateAttachments 导出附件限额校验（≤4 个、单文件 5MB、总 20MB、base64
// data URL），供内置 Agent 的 HTTP 层复用同一套规则。
func ValidateAttachments(value any) error {
	return validateAgentAttachments(value)
}

// decodedBase64Size 返回 base64 载荷的解码后字节数；字符集非法时 ok=false（不分配解码缓冲）
func decodedBase64Size(encoded string) (int, bool) {
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		valid := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '='
		if !valid {
			return 0, false
		}
	}
	padding := 0
	if strings.HasSuffix(encoded, "=") {
		padding++
		if strings.HasSuffix(encoded, "==") {
			padding++
		}
	}
	return len(encoded)/4*3 - padding, true
}

func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request, confFile *config.ConfigFile) {
	if confFile != nil {
		h.backgroundDir(confFile)
	}
	settings := Settings{AllowedSiteIDs: []string{}, AllowedSiteModelIDs: []string{}, ImageModels: []ImageModelSelection{}, PromptSuggestionEnabled: true, SitePolicy: "allow_all", ModelPolicy: "allow_all", Appearance: defaultAppearanceSettings()}
	if confFile != nil {
		settings.AllowedSiteIDs = readStringList(confFile, "agent.allowed_site_ids")
		settings.AllowedSiteModelIDs = readStringList(confFile, "agent.allowed_site_model_ids")
		settings.ImageModels = ImageModelsFromConfig(confFile)
		settings.PromptSuggestionEnabled = PromptSuggestionEnabledFromConfig(confFile)
		settings.FastModel = FastModelFromConfig(confFile)
		settings.ClassifierModel = ClassifierModelFromConfig(confFile)
		if value, ok := confFile.Get("agent.site_policy"); ok {
			if policy, valid := value.(string); valid && (policy == "allow_all" || policy == "allow_list") {
				settings.SitePolicy = policy
			}
		}
		if value, ok := confFile.Get("agent.model_policy"); ok {
			if policy, valid := value.(string); valid && (policy == "allow_all" || policy == "allow_list") {
				settings.ModelPolicy = policy
			}
		}
		settings.Appearance = readAppearance(confFile)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": settings})
}

func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request, confFile *config.ConfigFile) {
	if confFile == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "config_unavailable", "config persistence is not available")
		return
	}
	var input settingsInput
	if err := httpx.DecodeJSONBody(r, &input); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if input.ClearRunner {
		// 旧 runner 连接键已无消费者,更新设置时顺手清理,避免旧部署升级后残留。
		if err := confFile.Delete("agent.runner_base_url"); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to clear Agent runner settings")
			return
		}
		if err := confFile.Delete("agent.runner_internal_token"); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to clear Agent runner settings")
			return
		}
	}
	scopeUpdated := false
	if input.AllowedSiteIDs != nil {
		if err := confFile.Set("agent.allowed_site_ids", normalizeStringList(*input.AllowedSiteIDs)); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent site settings")
			return
		}
		scopeUpdated = true
	}
	if input.AllowedSiteModelIDs != nil {
		if err := confFile.Set("agent.allowed_site_model_ids", normalizeStringList(*input.AllowedSiteModelIDs)); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent model settings")
			return
		}
		scopeUpdated = true
	}
	if input.ImageModels != nil {
		for _, selection := range *input.ImageModels {
			if utf8.RuneCountInString(strings.TrimSpace(selection.Model)) > 200 {
				httpx.Error(w, r, http.StatusBadRequest, "invalid_image_models", "each image model must not exceed 200 characters")
				return
			}
		}
		if err := confFile.Set("agent.image_models", normalizeImageModels(*input.ImageModels)); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent image model settings")
			return
		}
	}
	if input.PromptSuggestionEnabled != nil {
		if err := confFile.Set("agent.prompt_suggestion_enabled", *input.PromptSuggestionEnabled); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent prompt suggestion setting")
			return
		}
	}
	if input.FastModel != nil {
		fastModel := strings.TrimSpace(*input.FastModel)
		if utf8.RuneCountInString(fastModel) > 200 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_fast_model", "fast model must not exceed 200 characters")
			return
		}
		if err := confFile.Set("agent.fast_model", fastModel); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent fast model setting")
			return
		}
	}
	if input.ClassifierModel != nil {
		classifierModel := strings.TrimSpace(*input.ClassifierModel)
		if utf8.RuneCountInString(classifierModel) > 200 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_classifier_model", "classifier model must not exceed 200 characters")
			return
		}
		if err := confFile.Set("agent.classifier_model", classifierModel); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent classifier model setting")
			return
		}
	}
	if input.SitePolicy != nil {
		sitePolicy := *input.SitePolicy
		if sitePolicy != "allow_list" {
			sitePolicy = "allow_all"
		}
		if err := confFile.Set("agent.site_policy", sitePolicy); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent site policy")
			return
		}
		scopeUpdated = true
	}
	if input.ModelPolicy != nil {
		modelPolicy := *input.ModelPolicy
		if modelPolicy != "allow_list" {
			modelPolicy = "allow_all"
		}
		if err := confFile.Set("agent.model_policy", modelPolicy); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent model policy")
			return
		}
		scopeUpdated = true
	}
	if input.Appearance != nil {
		appearance, err := normalizeAppearance(*input.Appearance)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_appearance", err.Error())
			return
		}
		appearance, err = h.persistAppearanceImages(appearance, confFile)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_appearance", err.Error())
			return
		}
		if err := confFile.Set("agent.appearance", map[string]any{
			"background_image":         appearance.BackgroundImage,
			"custom_background_images": appearance.CustomBackgroundImages,
			"side_transparency":        appearance.SideTransparency,
			"side_brightness":          appearance.SideBrightness,
			"side_thickness":           appearance.SideThickness,
			"backdrop_blur":            appearance.BackdropBlur,
			"backdrop_dim":             appearance.BackdropDim,
		}); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save Agent appearance settings")
			return
		}
		_ = cleanupBackgroundFiles(h.backgroundDir(confFile), appearance)
	}
	if scopeUpdated {
		h.notifySettingsUpdated()
	}
	h.GetSettings(w, r, confFile)
}

func readAppearance(confFile *config.ConfigFile) AppearanceSettings {
	appearance := defaultAppearanceSettings()
	value, ok := confFile.Get("agent.appearance")
	if !ok {
		return appearance
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return appearance
	}
	stored := defaultAppearanceSettings()
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return appearance
	}
	if normalized, err := normalizeAppearance(stored); err == nil {
		return normalized
	}
	return appearance
}

func normalizeAppearance(value AppearanceSettings) (AppearanceSettings, error) {
	if strings.TrimSpace(value.BackgroundImage) == "" {
		value.BackgroundImage = defaultBackgroundImage
	}
	if isDataURLTooLarge(value.BackgroundImage) || len(value.BackgroundImage) > maxBackgroundImageBytes && !strings.HasPrefix(value.BackgroundImage, "data:image/") {
		return AppearanceSettings{}, errors.New("background image is too large")
	}
	if len(value.CustomBackgroundImages) > 20 {
		return AppearanceSettings{}, errors.New("too many custom background images")
	}
	for _, image := range value.CustomBackgroundImages {
		if isDataURLTooLarge(image) || len(image) > maxBackgroundImageBytes && !strings.HasPrefix(image, "data:image/") {
			return AppearanceSettings{}, errors.New("custom background image is too large")
		}
		if image != "" && !strings.HasPrefix(image, "data:image/") && !isBackgroundPath(image) {
			return AppearanceSettings{}, errors.New("custom background image must be a stored background path or image data URL")
		}
	}
	if value.BackgroundImage != defaultBackgroundImage && !strings.HasPrefix(value.BackgroundImage, "data:image/") && !isBackgroundPath(value.BackgroundImage) {
		return AppearanceSettings{}, errors.New("background image must be the default image or a stored background path")
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"side_transparency", value.SideTransparency},
		{"side_brightness", value.SideBrightness},
		{"side_thickness", value.SideThickness},
		{"backdrop_blur", value.BackdropBlur},
		{"backdrop_dim", value.BackdropDim},
	} {
		if item.value < 0 || item.value > 100 {
			return AppearanceSettings{}, errors.New(item.name + " must be between 0 and 100")
		}
	}
	return value, nil
}

func isDataURLTooLarge(value string) bool {
	if !strings.HasPrefix(value, "data:image/") {
		return false
	}
	_, encoded, ok := strings.Cut(value, ",")
	if !ok {
		return false
	}
	padding := 0
	if strings.HasSuffix(encoded, "=") {
		padding++
		if strings.HasSuffix(encoded, "==") {
			padding++
		}
	}
	return len(encoded)/4*3-padding > maxBackgroundImageBytes
}

const backgroundURLPrefix = "/api/v1/agent/backgrounds/"

func isBackgroundPath(value string) bool {
	name := strings.TrimPrefix(value, backgroundURLPrefix)
	return strings.HasPrefix(value, backgroundURLPrefix) && name != "" && filepath.Base(name) == name && strings.HasPrefix(name, "agent-bg-")
}

func (h *Handler) backgroundDir(confFile *config.ConfigFile) string {
	if h.workdir != "" {
		return filepath.Join(h.workdir, "assets", "agent-backgrounds")
	}
	if confFile != nil {
		dir := filepath.Join(filepath.Dir(filepath.Dir(confFile.Path())), "assets", "agent-backgrounds")
		h.mu.Lock()
		h.backgroundDirFallback = dir
		h.mu.Unlock()
		return dir
	}
	h.mu.RLock()
	dir := h.backgroundDirFallback
	h.mu.RUnlock()
	return dir
}

func decodeBackgroundDataURL(value string) ([]byte, string, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.Contains(header, ";base64") {
		return nil, "", errors.New("custom background image must be a base64 image data URL")
	}
	mimeType := strings.TrimPrefix(strings.SplitN(header, ";", 2)[0], "data:")
	extension := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"image/avif": ".avif",
	}[mimeType]
	if extension == "" {
		return nil, "", errors.New("unsupported custom background image type")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return nil, "", errors.New("custom background image data is invalid")
	}
	if len(data) > maxBackgroundImageBytes {
		return nil, "", errors.New("custom background image is too large")
	}
	return data, extension, nil
}

func (h *Handler) persistAppearanceImages(value AppearanceSettings, confFile *config.ConfigFile) (AppearanceSettings, error) {
	dir := h.backgroundDir(confFile)
	if dir == "" {
		return AppearanceSettings{}, errors.New("background image storage is unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AppearanceSettings{}, errors.New("failed to create background image storage")
	}
	if strings.HasPrefix(value.BackgroundImage, "data:image/") {
		found := false
		for _, image := range value.CustomBackgroundImages {
			if image == value.BackgroundImage {
				found = true
				break
			}
		}
		if !found {
			if len(value.CustomBackgroundImages) >= 20 {
				return AppearanceSettings{}, errors.New("too many custom background images")
			}
			value.CustomBackgroundImages = append(value.CustomBackgroundImages, value.BackgroundImage)
		}
	}
	stored := make(map[string]string, len(value.CustomBackgroundImages))
	for _, image := range value.CustomBackgroundImages {
		if !strings.HasPrefix(image, "data:image/") {
			continue
		}
		data, extension, err := decodeBackgroundDataURL(image)
		if err != nil {
			return AppearanceSettings{}, err
		}
		digest := sha256.Sum256(data)
		name := "agent-bg-" + hex.EncodeToString(digest[:8]) + extension
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return AppearanceSettings{}, errors.New("failed to save background image")
		}
		stored[image] = backgroundURLPrefix + name
	}
	for index, image := range value.CustomBackgroundImages {
		if replacement, ok := stored[image]; ok {
			value.CustomBackgroundImages[index] = replacement
		}
	}
	if replacement, ok := stored[value.BackgroundImage]; ok {
		value.BackgroundImage = replacement
	}
	return value, nil
}

func cleanupBackgroundFiles(dir string, appearance AppearanceSettings) error {
	if dir == "" {
		return nil
	}
	keep := make(map[string]struct{}, len(appearance.CustomBackgroundImages)+1)
	for _, image := range appearance.CustomBackgroundImages {
		if isBackgroundPath(image) {
			keep[filepath.Base(strings.TrimPrefix(image, backgroundURLPrefix))] = struct{}{}
		}
	}
	if isBackgroundPath(appearance.BackgroundImage) {
		keep[filepath.Base(strings.TrimPrefix(appearance.BackgroundImage, backgroundURLPrefix))] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-bg-") {
			continue
		}
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (h *Handler) ServeBackground(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if filepath.Base(name) != name || !strings.HasPrefix(name, "agent-bg-") {
		http.NotFound(w, r)
		return
	}
	dir := h.backgroundDir(nil)
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(dir, name)
	// 文件名带内容哈希（agent-bg-<sha256>），内容变则文件名变，可安全长缓存
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

// SetOnSettingsUpdated registers a hook invoked after settings persist, so the
// agent LLM entrypoint can resync the internal gateway key policy.
func (h *Handler) SetOnSettingsUpdated(fn func()) {
	h.mu.Lock()
	h.onSettingsUpdated = fn
	h.mu.Unlock()
}

func (h *Handler) notifySettingsUpdated() {
	h.mu.RLock()
	fn := h.onSettingsUpdated
	h.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// AccessPolicyFromConfig reads the agent site/model allowlist settings from
// the config file so they can be synced onto the internal gateway key.
func AccessPolicyFromConfig(confFile *config.ConfigFile) (sitePolicy, modelPolicy string, siteIDs, siteModelIDs []string) {
	sitePolicy, modelPolicy = "allow_all", "allow_all"
	siteIDs, siteModelIDs = []string{}, []string{}
	if confFile == nil {
		return sitePolicy, modelPolicy, siteIDs, siteModelIDs
	}
	siteIDs = readStringList(confFile, "agent.allowed_site_ids")
	siteModelIDs = readStringList(confFile, "agent.allowed_site_model_ids")
	if value, ok := confFile.Get("agent.site_policy"); ok {
		if policy, valid := value.(string); valid && policy == "allow_list" {
			sitePolicy = policy
		}
	}
	if value, ok := confFile.Get("agent.model_policy"); ok {
		if policy, valid := value.(string); valid && policy == "allow_list" {
			modelPolicy = policy
		}
	}
	return sitePolicy, modelPolicy, siteIDs, siteModelIDs
}

// FastModelFromConfig 读取输入预测快速模型;未配置时返回空串。
func FastModelFromConfig(confFile *config.ConfigFile) string {
	if confFile == nil {
		return ""
	}
	value, ok := confFile.Get("agent.fast_model")
	if !ok {
		return ""
	}
	model, _ := value.(string)
	return strings.TrimSpace(model)
}

// ClassifierModelFromConfig 读取审批分类器模型;未配置时返回空串。
func ClassifierModelFromConfig(confFile *config.ConfigFile) string {
	if confFile == nil {
		return ""
	}
	value, ok := confFile.Get("agent.classifier_model")
	if !ok {
		return ""
	}
	model, _ := value.(string)
	return strings.TrimSpace(model)
}

// PromptSuggestionEnabledFromConfig 读取输入预测开关;未配置时默认开启。
func PromptSuggestionEnabledFromConfig(confFile *config.ConfigFile) bool {
	if confFile == nil {
		return true
	}
	value, ok := confFile.Get("agent.prompt_suggestion_enabled")
	if !ok {
		return true
	}
	enabled, valid := value.(bool)
	return !valid || enabled
}

func ImageModelsFromConfig(confFile *config.ConfigFile) []ImageModelSelection {
	if confFile == nil {
		return []ImageModelSelection{}
	}
	value, ok := confFile.Get("agent.image_models")
	if !ok {
		return []ImageModelSelection{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return []ImageModelSelection{}
	}
	var models []ImageModelSelection
	if err := json.Unmarshal(data, &models); err != nil {
		return []ImageModelSelection{}
	}
	return normalizeImageModels(models)
}

func normalizeImageModels(models []ImageModelSelection) []ImageModelSelection {
	result := make([]ImageModelSelection, 0, len(models))
	indexes := make(map[string]int, len(models))
	for _, selection := range models {
		selection.Model = strings.TrimSpace(selection.Model)
		if selection.Model == "" {
			continue
		}
		siteIDs := make([]string, 0, len(selection.SiteIDs))
		seenSites := make(map[string]struct{}, len(selection.SiteIDs))
		for _, rawSiteID := range selection.SiteIDs {
			siteID, err := uuid.Parse(strings.TrimSpace(rawSiteID))
			if err != nil {
				continue
			}
			text := siteID.String()
			if _, exists := seenSites[text]; exists {
				continue
			}
			seenSites[text] = struct{}{}
			siteIDs = append(siteIDs, text)
		}
		if index, exists := indexes[selection.Model]; exists {
			current := &result[index]
			if len(current.SiteIDs) == 0 || len(siteIDs) == 0 {
				current.SiteIDs = []string{}
				continue
			}
			seenSites = make(map[string]struct{}, len(current.SiteIDs)+len(siteIDs))
			for _, siteID := range current.SiteIDs {
				seenSites[siteID] = struct{}{}
			}
			for _, siteID := range siteIDs {
				if _, exists := seenSites[siteID]; !exists {
					current.SiteIDs = append(current.SiteIDs, siteID)
				}
			}
			continue
		}
		indexes[selection.Model] = len(result)
		result = append(result, ImageModelSelection{Model: selection.Model, SiteIDs: siteIDs})
	}
	return result
}

func readStringList(confFile *config.ConfigFile, key string) []string {
	value, ok := confFile.Get(key)
	if !ok {
		return []string{}
	}
	result := make([]string, 0)
	switch items := value.(type) {
	case []string:
		result = append(result, items...)
	case []any:
		for _, item := range items {
			if text, valid := item.(string); valid {
				result = append(result, text)
			}
		}
	default:
		return []string{}
	}
	return normalizeStringList(result)
}

func normalizeStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
