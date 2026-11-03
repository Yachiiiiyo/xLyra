// reference.go 把被引用会话的当前分支渲染成纯文本引用块,供发送消息时拼进
// 提示词。引用内容属于不可信输入(来自另一个会话的模型输出/文件内容),
// 渲染只做只读整理,调用方负责在提示里声明“仅供参考”。
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 引用渲染的默认上限。
const (
	// referenceSessionMaxRunes 是单个被引用会话渲染结果的上限。
	referenceSessionMaxRunes = 24000
	// referenceTotalMaxRunes 是多个引用合计的上限。
	referenceTotalMaxRunes = 60000
	// referenceToolRunes 是单条工具输出(与 compaction 摘要)保留的上限。
	referenceToolRunes = 400
	// referenceArgRunes 是工具调用参数提示的截断长度。
	referenceArgRunes = 40
)

// truncationMarker 是截断长内容时的统一后缀。
const truncationMarker = "…[已截断]"

// RenderReferenceOptions 控制单个引用的渲染;MaxChars 为渲染结果(含标签)
// 的字符上限,0 表示用默认 24000。
type RenderReferenceOptions struct {
	MaxChars int
}

// RenderReference 读取被引用会话的当前分支,渲染成一段
// <referenced_session>…</referenced_session> 文本:用户/助手/工具行按时间
// 排列,system 与 model_change 等元条目跳过,compaction 摘要作为一条
// [摘要] 行。用户消息文本优先取 meta 里保存的原文(Pi 文件里可能拼有引用
// 块或附件正文,不属于用户的原话)。超出上限时优先保留最新的消息,被丢弃
// 的最早若干条以一行省略提示代替。被引用会话正在运行时照读当前文件
// (只读,不加锁等待)。
func (s *Store) RenderReference(id string, opts RenderReferenceOptions) (string, error) {
	if !validID(id) {
		return "", ErrInvalidID
	}
	meta, err := readMeta(filepath.Join(s.root, id, metaFile))
	if err != nil {
		return "", err
	}
	var lines []string
	if file, err := PiSessionFile(s.PiDir(id), meta.PiSessionID); err == nil {
		entries, err := readPiEntries(file)
		if err != nil {
			return "", err
		}
		lines = renderReferenceLines(activePath(entries), meta)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	limit := opts.MaxChars
	if limit <= 0 {
		limit = referenceSessionMaxRunes
	}
	header := fmt.Sprintf("<referenced_session id=%q title=%q>", id, meta.DisplayTitle())
	return assembleReference(header, lines, limit), nil
}

// RenderReferences 按引用顺序渲染多个会话:总字符数按引用个数平均分配
// (默认合计 60000,单个仍不超过 24000),返回以空行分隔的引用块文本。
func (s *Store) RenderReferences(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	share := referenceTotalMaxRunes / len(ids)
	if share > referenceSessionMaxRunes {
		share = referenceSessionMaxRunes
	}
	blocks := make([]string, 0, len(ids))
	for _, id := range ids {
		block, err := s.RenderReference(id, RenderReferenceOptions{MaxChars: share})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n"), nil
}

// renderReferenceLines 把当前分支条目整理成展示行(时间顺序)。
func renderReferenceLines(path []piEntry, meta Meta) []string {
	var lines []string
	toolHints := map[string]string{} // toolCallID → "工具名 参数提示"
	for _, entry := range path {
		switch entry.Type {
		case "compaction":
			lines = append(lines, "[摘要] "+truncateMarked(entry.Summary, referenceToolRunes))
		case "message":
			if entry.Message == nil {
				continue
			}
			switch entry.Message.Role {
			case "user":
				text, images := splitContent(entry.Message.Content)
				// 展示文本优先取 meta 原文,理由同 transcript。
				if bound := bindingFor(meta, entry.ID); bound != nil && bound.Text != "" {
					text = bound.Text
				}
				parts := make([]string, 0, 1+len(images))
				if text != "" {
					parts = append(parts, text)
				}
				for range images {
					parts = append(parts, "[图片]")
				}
				if len(parts) > 0 {
					lines = append(lines, "[用户] "+strings.Join(parts, "\n"))
				}
			case "assistant":
				text, _, calls := splitAssistant(entry.Message.Content)
				for _, call := range calls {
					callID, _ := call["id"].(string)
					if callID == "" {
						continue
					}
					name, _ := call["name"].(string)
					rawArgs, _ := call["raw_arguments"].(string)
					toolHints[callID] = strings.TrimSpace(name + " " + referenceArgHint(rawArgs))
				}
				if text != "" {
					lines = append(lines, "[助手] "+text)
				}
			case "toolResult":
				text, _ := splitContent(entry.Message.Content)
				label := entry.Message.ToolName
				if hint, ok := toolHints[entry.Message.ToolCallID]; ok && hint != "" {
					label = hint
				}
				lines = append(lines, fmt.Sprintf("[工具 %s] %s", label, truncateMarked(text, referenceToolRunes)))
				for i, image := range toolImages(entry.Message.Details) {
					if i == 4 {
						break
					}
					lines = append(lines, fmt.Sprintf("[图片 %s]", image["path"]))
				}
			}
		}
	}
	return lines
}

// referenceArgHint 从工具调用参数里取一个短提示(path/command 等常见字段),
// 便于在 [工具 …] 行里辨认调用对象。
func referenceArgHint(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "file", "command", "pattern", "query", "url"} {
		if value, ok := args[key].(string); ok && value != "" {
			return truncateRunes(value, referenceArgRunes)
		}
	}
	return ""
}

// assembleReference 组装引用块:行文本超预算时从最早的行开始丢弃,并在顶部
// 加一行 [已省略更早的 N 条消息];连最新一行都放不下时截断该行本身。
func assembleReference(header string, lines []string, limit int) string {
	const footer = "</referenced_session>"
	omissionLine := func(n int) string {
		return fmt.Sprintf("[已省略更早的 %d 条消息]", n)
	}
	body := strings.Join(lines, "\n")
	budget := limit - utf8.RuneCountInString(header) - utf8.RuneCountInString(footer) - 2
	if budget < 0 {
		budget = 0
	}
	if utf8.RuneCountInString(body) <= budget {
		return header + "\n" + body + "\n" + footer
	}
	var kept []string
	size := 0
	i := len(lines) - 1
	for i >= 0 {
		cost := utf8.RuneCountInString(lines[i]) + 1 // 行内容 + 换行
		reserve := utf8.RuneCountInString(omissionLine(i)) + 1
		if size+cost+reserve > budget {
			break
		}
		kept = append([]string{lines[i]}, kept...)
		size += cost
		i--
	}
	if len(kept) == 0 {
		// 预算容不下任何完整行:保留被截断的最新一行。
		last := ""
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		return header + "\n" + truncateInclusive(last, budget) + "\n" + footer
	}
	body = omissionLine(i+1) + "\n" + strings.Join(kept, "\n")
	return header + "\n" + body + "\n" + footer
}

// truncateMarked 把内容截到 max 个字符,超出时以截断标记结尾
// (标记不计入 max)。
func truncateMarked(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + truncationMarker
}

// truncateInclusive 把内容截到不超过 max 个字符(含截断标记本身)。
func truncateInclusive(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := max - utf8.RuneCountInString(truncationMarker)
	if keep <= 0 {
		return string([]rune(s)[:max])
	}
	return string([]rune(s)[:keep]) + truncationMarker
}
