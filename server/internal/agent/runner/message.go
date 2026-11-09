package runner

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"xlyra/server/internal/agent/piconfig"
	"xlyra/server/internal/agent/session"
)

const textAttachmentLimit = 200 * 1024

func (r *Runner) validateMessage(content, model, permissionMode, effort string, attachments []Attachment) (runSpec, []session.Attachment, error) {
	model = strings.TrimSpace(model)
	if strings.TrimSpace(content) == "" {
		return runSpec{}, nil, fmt.Errorf("%w: 内容不能为空", ErrInvalid)
	}
	if model == "" {
		return runSpec{}, nil, fmt.Errorf("%w: 模型不能为空", ErrInvalid)
	}
	if permissionMode == "" {
		permissionMode = "ask"
	}
	if permissionMode != "ask" && permissionMode != "auto" && permissionMode != "full" {
		return runSpec{}, nil, fmt.Errorf("%w: 未知权限模式 %q", ErrInvalid, permissionMode)
	}
	spec := runSpec{model: model, effort: effort, mode: permissionMode, text: content}
	metaAttachments := make([]session.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		mimeType, data, err := decodeDataURL(attachment)
		if err != nil {
			return runSpec{}, nil, err
		}
		metaAttachments = append(metaAttachments, session.Attachment{Name: attachment.Name, MimeType: mimeType})
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			if !piconfig.CapsFor(model).Image {
				return runSpec{}, nil, fmt.Errorf("%w: 模型 %s 不支持图片附件", ErrInvalid, model)
			}
			spec.images = append(spec.images, map[string]any{
				"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mimeType,
			})
		case isTextMime(mimeType):
			text := string(data)
			if len(data) > textAttachmentLimit {
				text = string(data[:textAttachmentLimit]) + "\n…(附件超过 200KB,已截断)"
			}
			spec.text += "\n\n[附件 " + attachment.Name + "]\n" + text
		default:
			return runSpec{}, nil, fmt.Errorf("%w: 不支持的附件类型 %s", ErrInvalid, mimeType)
		}
	}
	return spec, metaAttachments, nil
}

func decodeDataURL(attachment Attachment) (string, []byte, error) {
	header, encoded, ok := strings.Cut(attachment.DataURL, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", nil, fmt.Errorf("%w: 附件 %s 不是 base64 data URL", ErrInvalid, attachment.Name)
	}
	mimeType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if attachment.MimeType != "" {
		mimeType = attachment.MimeType
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("%w: 附件 %s 的 base64 无效", ErrInvalid, attachment.Name)
	}
	return mimeType, data, nil
}

func isTextMime(mimeType string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/javascript":
		return true
	default:
		return false
	}
}

// originalAttachments 只恢复 Pi 会话文件里仍保存的图片;文本附件的原内容不复制到
// meta,取不到时按任务约定仅重发文本。
func (r *Runner) originalAttachments(meta session.Meta, message session.UserMessage) []Attachment {
	if message.EntryID == "" {
		return nil
	}
	path, err := session.PiSessionFile(r.cfg.Store.PiDir(meta.ID), meta.PiSessionID)
	if err != nil {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var entry struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "message" && entry.ID == message.EntryID && entry.Message.Role == "user" {
				var blocks []struct {
					Type     string `json:"type"`
					Data     string `json:"data"`
					MimeType string `json:"mimeType"`
				}
				if json.Unmarshal(entry.Message.Content, &blocks) != nil {
					return nil
				}
				var attachments []Attachment
				for _, block := range blocks {
					if block.Type != "image" {
						continue
					}
					name := fmt.Sprintf("image-%d", len(attachments))
					if len(attachments) < len(message.Attachments) && message.Attachments[len(attachments)].Name != "" {
						name = message.Attachments[len(attachments)].Name
					}
					attachments = append(attachments, Attachment{
						Name: name, MimeType: block.MimeType, DataURL: "data:" + block.MimeType + ";base64," + block.Data,
					})
				}
				return attachments
			}
		}
		if readErr != nil {
			return nil
		}
	}
}
