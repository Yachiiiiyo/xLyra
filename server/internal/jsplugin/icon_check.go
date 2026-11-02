package jsplugin

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"math"
	"path"
	"strings"
)

const (
	iconOpaqueAlpha = 250
	// 0.13 对应图标底板 #181b22 上 WCAG 3:1 对比度。
	minIconLuminance     = 0.13
	minIconOpaquePercent = 98
	minPluginIconSide    = 64
	maxPluginIconSide    = 1024
)

// IconReport is the measurement verify prints for a top-level icon.
type IconReport struct {
	Path        string
	Width       int
	Height      int
	OpaqueRatio float64
	Luminance   float64
}

// CheckIconLegibility measures the top-level icon against the dark plate.
// A manifest with no top-level icon returns a zero report. The check stays
// out of ReadPackage so reloading a stored package does not pick up a later
// threshold change. Call it wherever a package is published: upload, market
// install, and the CLI.
func CheckIconLegibility(raw []byte, manifest Manifest) (IconReport, error) {
	icon := strings.TrimSpace(manifest.Icon)
	if icon == "" {
		return IconReport{}, nil
	}
	cleaned, err := pluginIconPath(icon)
	if err != nil {
		return IconReport{}, err
	}
	data, err := packageFile(raw, cleaned)
	if err != nil {
		return IconReport{}, err
	}
	// 先看声明的尺寸：一个很小的 PNG 可以声明巨大的画布，不能直接解码。
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return IconReport{}, fmt.Errorf("icon 不是合法的 PNG")
	}
	if cfg.Width != cfg.Height || cfg.Width < minPluginIconSide || cfg.Width > maxPluginIconSide {
		return IconReport{}, fmt.Errorf("icon 必须是 %d–%d 像素的方形，现在是 %d×%d", minPluginIconSide, maxPluginIconSide, cfg.Width, cfg.Height)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return IconReport{}, fmt.Errorf("icon 不是合法的 PNG")
	}
	bounds := img.Bounds()
	var opaque, total int
	var weighted, weight float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			total++
			if c.A >= iconOpaqueAlpha {
				opaque++
			}
			if c.A == 0 {
				continue
			}
			w := float64(c.A) / 255
			weighted += relativeLuminance(c) * w
			weight += w
		}
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(opaque) / float64(total)
	}
	lum := 0.0
	if weight > 0 {
		lum = weighted / weight
	}
	report := IconReport{
		Path:        cleaned,
		Width:       bounds.Dx(),
		Height:      bounds.Dy(),
		OpaqueRatio: ratio,
		Luminance:   lum,
	}
	if weight == 0 {
		return report, fmt.Errorf("图标是空的")
	}
	if total > 0 && opaque*100 < total*minIconOpaquePercent && lum < minIconLuminance {
		return report, fmt.Errorf("图标有透明背景且图形偏暗，在 xLyra 的深色界面上看不清（平均亮度 %.2f，要求不低于 %.2f）。请改用不透明底色的方形图标，或使用浅色/明亮的图形", lum, minIconLuminance)
	}
	return report, nil
}

func validatePluginIcon(icon string, files map[string][]byte) error {
	icon = strings.TrimSpace(icon)
	if icon == "" {
		return nil
	}
	cleaned, err := pluginIconPath(icon)
	if err != nil {
		return err
	}
	data, ok := files[cleaned]
	if !ok {
		return fmt.Errorf("icon %q 不在包内", cleaned)
	}
	return ValidateIconBytes(data)
}

// ValidateIconBytes checks a top-level icon file: PNG magic, at most 64 KiB,
// square, and a side between 64 and 1024 pixels.
func ValidateIconBytes(data []byte) error {
	if len(data) > maxSiteProviderIconBytes {
		return fmt.Errorf("icon 不能超过 %d 字节", maxSiteProviderIconBytes)
	}
	if !siteProviderIconMagic(".png", data) {
		return fmt.Errorf("icon 不是合法的 PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("icon 不是合法的 PNG")
	}
	if cfg.Width != cfg.Height {
		return fmt.Errorf("icon 必须是方形，现在是 %d×%d", cfg.Width, cfg.Height)
	}
	if cfg.Width < minPluginIconSide || cfg.Width > maxPluginIconSide {
		return fmt.Errorf("icon 边长必须在 %d–%d 之间，现在是 %d×%d", minPluginIconSide, maxPluginIconSide, cfg.Width, cfg.Height)
	}
	return nil
}

func pluginIconPath(icon string) (string, error) {
	if strings.Contains(icon, `\`) || strings.Contains(icon, ":") || strings.HasPrefix(icon, "/") {
		return "", fmt.Errorf("icon %q 必须是包内相对路径", icon)
	}
	cleaned := path.Clean(icon)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("icon %q 必须是包内相对路径", icon)
	}
	if path.Ext(cleaned) != ".png" {
		return "", fmt.Errorf("icon 只能是 .png 文件")
	}
	return cleaned, nil
}

func relativeLuminance(c color.NRGBA) float64 {
	return 0.2126*linearChannel(c.R) + 0.7152*linearChannel(c.G) + 0.0722*linearChannel(c.B)
}

func linearChannel(channel uint8) float64 {
	c := float64(channel) / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}
