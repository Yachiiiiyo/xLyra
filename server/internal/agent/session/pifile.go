package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// previewWindow 是 Preview 只读文件尾部的最大字节数。
const previewWindow = 64 * 1024

// PiSessionFile 在 Pi 会话目录里定位会话文件:piSessionID 非空时按
// 「_<piSessionID>.jsonl」后缀匹配,为空时取目录里最新的会话文件
// (文件名以 ISO 时间戳前缀开头,字典序即时间序);找不到返回错误。
// 同一 id 命中多个文件时取最新(正常情况下一个 id 只有一个文件)。
func PiSessionFile(dir, piSessionID string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	best := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if piSessionID != "" && !strings.HasSuffix(name, "_"+piSessionID+".jsonl") {
			continue
		}
		if name > best {
			best = name
		}
	}
	if best == "" {
		if piSessionID == "" {
			return "", fmt.Errorf("session: %s 下没有 Pi 会话文件: %w", dir, os.ErrNotExist)
		}
		return "", fmt.Errorf("session: %s 下没有 id 为 %s 的 Pi 会话文件: %w", dir, piSessionID, os.ErrNotExist)
	}
	return filepath.Join(dir, best), nil
}

// lastEntryWindow 是 LastEntryID 只读文件尾部的最大字节数。
const lastEntryWindow = 256 * 1024

// LastEntryID 返回会话当前 Pi 会话文件里最后一个带 id 的条目 id,作审批
// 与 run 失败记录的锚点兜底:Pi 1.4.0 的 entry_appended 只在扩展追加自定义
// 条目时触发,普通用户/assistant/toolResult 条目不发该事件,事件循环里记
// 到的"最后条目 id"常为空。只读文件尾部(至多 lastEntryWindow 字节);
// 会话文件不存在(尚未跑过 Pi)或窗口内没有带 id 的条目时返回空串且不
// 报错。fork 后会话文件会换,这里每次重读 meta 取调用时刻的 PiSessionID。
func (s *Store) LastEntryID(id string) (string, error) {
	if !validID(id) {
		return "", ErrInvalidID
	}
	meta, err := s.Get(id)
	if err != nil {
		return "", err
	}
	file, err := PiSessionFile(s.PiDir(id), meta.PiSessionID)
	if err != nil {
		return "", nil // 尚未跑过 Pi:没有锚点,不算错误
	}
	lines, err := readTailLines(file, lastEntryWindow)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil // 定位后文件被删(会话清理):同样不算错误
		}
		return "", err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var entry piEntry
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &entry) != nil {
			continue
		}
		// 会话头(type=session)的 id 是 Pi 会话 id,不是条目 id:与
		// readPiEntries 同样跳过。
		if entry.Type != "" && entry.Type != "session" && entry.ID != "" {
			return entry.ID, nil
		}
	}
	return "", nil
}

// readTailLines 读取文件尾部至多 maxBytes 字节,返回窗口内的完整行
// (不带换行符)。窗口起点落在文件中间时,被截断的首行丢弃;读取中遇到
// 的坏行由调用方解析时跳过。
func readTailLines(path string, maxBytes int64) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var offset int64
	if size := info.Size(); size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(file)
	var lines [][]byte
	first := true
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			content := line
			if err == nil { // 仅在读到换行符时去掉它
				content = line[:len(line)-1]
			}
			// 起点在文件中间:首行多半是半行,宁可丢弃
			if !first || offset == 0 {
				lines = append(lines, content)
			}
		}
		first = false
		if err != nil {
			break
		}
	}
	return lines, nil
}

// previewFromLines 从尾部向前找最后一条 assistant/用户文本,截取前 80 rune。
func previewFromLines(lines [][]byte) string {
	for i := len(lines) - 1; i >= 0; i-- {
		var entry piEntry
		if err := json.Unmarshal(bytes.TrimSpace(lines[i]), &entry); err != nil {
			continue
		}
		if entry.Type != "message" || entry.Message == nil {
			continue
		}
		switch entry.Message.Role {
		case "user", "assistant":
		default:
			continue
		}
		text, _ := splitContent(entry.Message.Content)
		if text != "" {
			return truncateRunes(text, previewMaxRunes)
		}
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
