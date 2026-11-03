package settings

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpx"
)

// AppearanceConfig 是全实例的外观配置：一张背景图（空串 = 星点背景）与自定义图库。
// 存储与校验规则沿用 agentproxy 的外观实现：自定义图片以 base64 data URL 上传，
// 按内容 sha256 落盘后以 /api/v1/appearance/backgrounds/ 路径引用。
type AppearanceConfig struct {
	BackgroundImage        string   `json:"background_image"`
	CustomBackgroundImages []string `json:"custom_background_images"`
}

const (
	appearanceConfigPath     = "appearance"
	maxAppearanceImageBytes  = 8 * 1024 * 1024
	maxAppearanceGallerySize = 20
	appearanceFilePrefix     = "appearance-bg-"
)

const appearanceBackgroundURLPrefix = "/api/v1/appearance/backgrounds/"

func defaultAppearanceConfig() AppearanceConfig {
	return AppearanceConfig{BackgroundImage: "", CustomBackgroundImages: []string{}}
}

func (h Handler) GetAppearance(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, readAppearanceConfig(h.confFile))
}

func (h Handler) UpdateAppearance(w http.ResponseWriter, r *http.Request) {
	if h.confFile == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "config_unavailable", "config persistence is not available")
		return
	}
	var req AppearanceConfig
	if err := httpx.DecodeJSONBody(r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	next, err := normalizeAppearanceConfig(req)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_appearance", err.Error())
		return
	}
	next, err = persistAppearanceImages(next, h.confFile)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_appearance", err.Error())
		return
	}
	if err := h.confFile.Set(appearanceConfigPath, map[string]any{
		"background_image":         next.BackgroundImage,
		"custom_background_images": next.CustomBackgroundImages,
	}); err != nil {
		h.logError("update appearance config", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "config_write_error", "failed to save config")
		return
	}
	_ = cleanupAppearanceBackgroundFiles(appearanceBackgroundDir(h.confFile), next)

	h.logInfo("appearance config updated", "background_image", next.BackgroundImage, "custom_images", len(next.CustomBackgroundImages))
	httpx.JSON(w, http.StatusOK, next)
}

func (h Handler) ServeAppearanceBackground(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if filepath.Base(name) != name || !strings.HasPrefix(name, appearanceFilePrefix) {
		http.NotFound(w, r)
		return
	}
	if h.confFile == nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(appearanceBackgroundDir(h.confFile), name)
	// 文件名带内容哈希（appearance-bg-<sha256>），内容变则文件名变，可安全长缓存
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func readAppearanceConfig(confFile *config.ConfigFile) AppearanceConfig {
	appearance := defaultAppearanceConfig()
	if confFile == nil {
		return appearance
	}
	value, ok := confFile.Get(appearanceConfigPath)
	if !ok {
		return appearance
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return appearance
	}
	stored := defaultAppearanceConfig()
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return appearance
	}
	if normalized, err := normalizeAppearanceConfig(stored); err == nil {
		return normalized
	}
	return appearance
}

func normalizeAppearanceConfig(value AppearanceConfig) (AppearanceConfig, error) {
	if value.CustomBackgroundImages == nil {
		value.CustomBackgroundImages = []string{}
	}
	if len(value.CustomBackgroundImages) > maxAppearanceGallerySize {
		return AppearanceConfig{}, errors.New("too many custom background images")
	}
	for _, image := range value.CustomBackgroundImages {
		if isAppearanceDataURLTooLarge(image) {
			return AppearanceConfig{}, errors.New("custom background image is too large")
		}
		if !strings.HasPrefix(image, "data:image/") && !isAppearanceBackgroundPath(image) {
			return AppearanceConfig{}, errors.New("custom background image must be a stored background path or image data URL")
		}
	}
	if value.BackgroundImage != "" {
		found := false
		for _, image := range value.CustomBackgroundImages {
			if image == value.BackgroundImage {
				found = true
				break
			}
		}
		if !found {
			return AppearanceConfig{}, errors.New("background image must be empty or one of the custom background images")
		}
	}
	return value, nil
}

func isAppearanceDataURLTooLarge(value string) bool {
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
	return len(encoded)/4*3-padding > maxAppearanceImageBytes
}

func isAppearanceBackgroundPath(value string) bool {
	name := strings.TrimPrefix(value, appearanceBackgroundURLPrefix)
	return strings.HasPrefix(value, appearanceBackgroundURLPrefix) && name != "" && filepath.Base(name) == name && strings.HasPrefix(name, appearanceFilePrefix)
}

// appearanceBackgroundDir 与 agentproxy 的 backgroundDir 推导方式一致：配置文件
// （<workdir>/conf/config.json）同级的 assets/ 下。
func appearanceBackgroundDir(confFile *config.ConfigFile) string {
	if confFile == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(confFile.Path())), "assets", "appearance-backgrounds")
}

func decodeAppearanceDataURL(value string) ([]byte, string, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.Contains(header, ";base64") {
		return nil, "", errors.New("custom background image must be a base64 image data URL")
	}
	mimeType := strings.TrimPrefix(strings.SplitN(header, ";", 2)[0], "data:")
	extension := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	}[mimeType]
	if extension == "" {
		return nil, "", errors.New("unsupported custom background image type")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return nil, "", errors.New("custom background image data is invalid")
	}
	if len(data) > maxAppearanceImageBytes {
		return nil, "", errors.New("custom background image is too large")
	}
	return data, extension, nil
}

func persistAppearanceImages(value AppearanceConfig, confFile *config.ConfigFile) (AppearanceConfig, error) {
	dir := appearanceBackgroundDir(confFile)
	if dir == "" {
		return AppearanceConfig{}, errors.New("background image storage is unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AppearanceConfig{}, errors.New("failed to create background image storage")
	}
	stored := make(map[string]string, len(value.CustomBackgroundImages))
	for _, image := range value.CustomBackgroundImages {
		if !strings.HasPrefix(image, "data:image/") {
			continue
		}
		data, extension, err := decodeAppearanceDataURL(image)
		if err != nil {
			return AppearanceConfig{}, err
		}
		digest := sha256.Sum256(data)
		name := appearanceFilePrefix + hex.EncodeToString(digest[:8]) + extension
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return AppearanceConfig{}, errors.New("failed to save background image")
		}
		stored[image] = appearanceBackgroundURLPrefix + name
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

func cleanupAppearanceBackgroundFiles(dir string, appearance AppearanceConfig) error {
	if dir == "" {
		return nil
	}
	keep := make(map[string]struct{}, len(appearance.CustomBackgroundImages)+1)
	for _, image := range appearance.CustomBackgroundImages {
		if isAppearanceBackgroundPath(image) {
			keep[filepath.Base(strings.TrimPrefix(image, appearanceBackgroundURLPrefix))] = struct{}{}
		}
	}
	if isAppearanceBackgroundPath(appearance.BackgroundImage) {
		keep[filepath.Base(strings.TrimPrefix(appearance.BackgroundImage, appearanceBackgroundURLPrefix))] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), appearanceFilePrefix) {
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
