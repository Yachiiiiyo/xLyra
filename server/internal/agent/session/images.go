package session

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// toolImages 从 toolResult.details.images 提取可公开的图片元数据;无效项或
// 无效字段只丢弃自身,不让旧会话或扩展的自定义 details 影响转录。
func toolImages(details json.RawMessage) []map[string]any {
	var object map[string]json.RawMessage
	if json.Unmarshal(details, &object) != nil {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(object["images"], &items) != nil {
		return nil
	}
	images := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		var path string
		if json.Unmarshal(item["path"], &path) != nil || !validToolImagePath(path) {
			continue
		}
		var mime string
		_ = json.Unmarshal(item["mime"], &mime)
		switch mime {
		case "image/png", "image/jpeg", "image/webp":
		default:
			mime = ""
		}
		var size int64
		if json.Unmarshal(item["bytes"], &size) != nil || size < 0 {
			size = 0
		}
		images = append(images, map[string]any{
			"path":  path,
			"mime":  mime,
			"bytes": size,
			"model": toolImageString(item["model"]),
			"site":  toolImageString(item["site"]),
			"alt":   toolImageString(item["alt"]),
		})
	}
	return images
}

func validToolImagePath(path string) bool {
	return len(path) > 0 && len(path) <= 200 && strings.HasPrefix(path, "images/") &&
		!strings.Contains(path, "..") && !strings.Contains(path, `\`)
}

func toolImageString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if utf8.RuneCountInString(value) <= 200 {
		return value
	}
	return string([]rune(value)[:200])
}
