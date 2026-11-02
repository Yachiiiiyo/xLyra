package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

// ErrPluginIconNotFound means this plugin has no icon to serve.
var ErrPluginIconNotFound = errors.New("plugin icon not found")

// PluginIcon is the bitmap for one plugin version.
type PluginIcon struct {
	Bytes       []byte
	ContentType string
	SHA256      string
}

// PluginIcon loads the icon for pluginID. An enabled version wins; otherwise
// the latest uploaded version is used. A disabled plugin still has an icon.
// site_provider uses siteProvider.display.icon when that path is set, and the
// top-level icon otherwise. Other kinds use only the top-level icon.
// A missing plugin, a version with no icon, or an unreadable file returns
// ErrPluginIconNotFound.
func (m *Manager) PluginIcon(ctx context.Context, pluginID string) (PluginIcon, error) {
	if m == nil {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	headers, err := m.repo.ListVersionHeadersForPlugin(ctx, pluginID)
	if err != nil {
		return PluginIcon{}, err
	}
	row, ok := SelectPluginVersion(headers)
	if !ok {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	if len(row.Manifest) > 0 && !manifestJSONHasIcon(row.Manifest) {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	full, err := m.repo.GetVersion(ctx, row.PluginID, row.Version)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PluginIcon{}, ErrPluginIconNotFound
		}
		return PluginIcon{}, err
	}
	return pluginIconFromVersion(full)
}

// SelectPluginVersion prefers an enabled version. With none, it uses the
// latest upload (CreatedAt). Equal timestamps keep the earlier row.
func SelectPluginVersion(versions []store.JSPluginVersion) (store.JSPluginVersion, bool) {
	var enabled store.JSPluginVersion
	var latest store.JSPluginVersion
	var hasEnabled bool
	var hasLatest bool
	for _, row := range versions {
		if row.Status == store.JSPluginStatusEnabled && (!hasEnabled || row.CreatedAt.After(enabled.CreatedAt)) {
			enabled = row
			hasEnabled = true
		}
		if !hasLatest || row.CreatedAt.After(latest.CreatedAt) {
			latest = row
			hasLatest = true
		}
	}
	if hasEnabled {
		return enabled, true
	}
	return latest, hasLatest
}

// PluginIconURL is the installed-list icon URL for the selected version.
// It is empty when that version's stored manifest does not declare an icon.
func PluginIconURL(pluginID string, row store.JSPluginVersion) string {
	pluginID = strings.TrimSpace(pluginID)
	sum := strings.TrimSpace(row.PackageSHA256)
	if pluginID == "" || len(sum) < 16 || !manifestJSONHasIcon(row.Manifest) {
		return ""
	}
	return "/api/v1/plugin-icons/" + pluginID + "?v=" + sum[:16]
}

func pluginIconFromVersions(versions []store.JSPluginVersion) (PluginIcon, error) {
	row, ok := SelectPluginVersion(versions)
	if !ok {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	return pluginIconFromVersion(row)
}

func pluginIconFromVersion(row store.JSPluginVersion) (PluginIcon, error) {
	if len(row.Package) == 0 {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	pkg, err := ReadPackage(row.Package, true)
	if err != nil {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	iconPath, topLevel, ok := declaredIcon(pkg.Manifest)
	if !ok {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	var cleaned string
	if topLevel {
		cleaned, err = pluginIconPath(iconPath)
	} else {
		cleaned, err = siteProviderIconPath(iconPath)
	}
	if err != nil {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	data, err := packageFile(row.Package, cleaned)
	if err != nil || len(data) > maxSiteProviderIconBytes {
		return PluginIcon{}, ErrPluginIconNotFound
	}
	contentType := ""
	if topLevel {
		if !siteProviderIconMagic(".png", data) {
			return PluginIcon{}, ErrPluginIconNotFound
		}
		contentType = "image/png"
	} else {
		contentType = siteProviderIconContentType(data)
		if contentType == "" {
			return PluginIcon{}, ErrPluginIconNotFound
		}
	}
	return PluginIcon{Bytes: data, ContentType: contentType, SHA256: pkg.SHA256}, nil
}

func declaredIcon(manifest Manifest) (iconPath string, topLevel bool, ok bool) {
	if manifest.Kind == KindSiteProvider {
		if icon := strings.TrimSpace(manifest.SiteProvider.Display.Icon); icon != "" {
			return icon, false, true
		}
	}
	if icon := strings.TrimSpace(manifest.Icon); icon != "" {
		return icon, true, true
	}
	return "", false, false
}

func manifestJSONHasIcon(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return false
	}
	_, _, ok := declaredIcon(manifest)
	return ok
}

func siteProviderIconContentType(data []byte) string {
	switch {
	case siteProviderIconMagic(".png", data):
		return "image/png"
	case siteProviderIconMagic(".webp", data):
		return "image/webp"
	default:
		return ""
	}
}
