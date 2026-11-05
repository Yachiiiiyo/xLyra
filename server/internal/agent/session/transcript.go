package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// piEntry 是 Pi 会话文件(v3,树结构)里除文件头外的一种条目;字段按
// docs/session-format.md。只声明本包消费的字段,未知字段忽略。
type piEntry struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	ParentID  *string    `json:"parentId"`
	Timestamp string     `json:"timestamp"`
	Message   *piMessage `json:"message"`

	// compaction 条目
	Summary              string `json:"summary"`
	TokensBefore         *int64 `json:"tokensBefore"`
	TokensAfter          *int64 `json:"tokensAfter"`
	EstimatedTokensAfter *int64 `json:"estimatedTokensAfter"`
}

// piMessage 是条目内嵌的 AgentMessage;content 兼容 string 与内容块数组。
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	Details    json.RawMessage `json:"details"`
}

// piBlock 是 assistant/user/toolResult 的内容块。
type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Data      string          `json:"data"`
	MimeType  string          `json:"mimeType"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// BuildTranscript 读取 Pi 会话文件 + meta,生成与前端 AgentTranscriptEntry
// 一致的 transcript 条目序列:
//   - 遍历当前分支(最后一个条目为叶子,沿 parentId 回溯到根);
//   - message 条目按 role 转成 user/assistant/tool 消息,system 等其他角色
//     及 model_change 等非消息条目跳过(但仍占据锚点位置);
//   - compaction 条目按文件位置插入,展示完整历史而非压缩后的有效上下文;
//   - meta.Escalations 与 meta.RunErrors 按锚点插到对应 Pi 条目之后,
//     同一锚点下审批在前、错误在后;锚点为空或不在文件里(文件损坏/
//     还未落盘)时追加在末尾;锚点指向文件里存在但不在当前分支上的条目
//     (retry fork 后被抛弃的旧分支)时丢弃不展示;
//   - 会话文件不存在(尚未跑过 Pi)时只回放已记录的 run 失败。
//
// 非法行(截断的最后一行等)跳过不报错;单行可能很长,用 ReadBytes 逐行读。
func (s *Store) BuildTranscript(id string) ([]map[string]any, error) {
	if !validID(id) {
		return nil, ErrInvalidID
	}
	meta, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 16)
	file, err := PiSessionFile(s.PiDir(id), meta.PiSessionID)
	if err != nil {
		// 尚未跑过 Pi 时保持空 transcript;启动阶段失败的 run 没有会话
		// 文件,仍须把已记录的失败回放出来。
		for _, runErr := range meta.RunErrors {
			out = append(out, runErrorEntry(runErr))
		}
		return out, nil
	}
	entries, err := readPiEntries(file)
	if err != nil {
		return nil, err
	}
	path := activePath(entries)

	// 集合区分三种锚点:在当前分支上(按位置插入)、非空但不在当前分支
	// (只在文件里的旧分支,或 fork 之后新文件里已经没有的条目:都是被抛弃的
	// 旧分支,丢弃)、空锚点(锚点取不到的老记录,补到末尾)。非空锚点来自会话
	// 文件本身,所以"在文件里找不到"只可能是旧文件里的条目。
	allIDs := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID != "" {
			allIDs[entry.ID] = true
		}
	}
	onPath := make(map[string]bool, len(path))
	for _, entry := range path {
		onPath[entry.ID] = true
	}
	afterEsc := map[string][]Escalation{}
	var tailEsc []Escalation
	for _, esc := range meta.Escalations {
		switch {
		case esc.AfterEntryID != "" && onPath[esc.AfterEntryID]:
			afterEsc[esc.AfterEntryID] = append(afterEsc[esc.AfterEntryID], esc)
		case esc.AfterEntryID == "":
			tailEsc = append(tailEsc, esc)
		} // 其余:旧分支上的审批,丢弃
	}
	afterErr := map[string][]RunError{}
	var tailErr []RunError
	for _, runErr := range meta.RunErrors {
		switch {
		case runErr.AfterEntryID != "" && onPath[runErr.AfterEntryID]:
			afterErr[runErr.AfterEntryID] = append(afterErr[runErr.AfterEntryID], runErr)
		case runErr.AfterEntryID == "":
			tailErr = append(tailErr, runErr)
		} // 其余:旧分支上的错误,丢弃
	}
	afterChange := map[string][]RunChange{}
	var tailChange []RunChange
	for _, rc := range meta.RunChanges {
		switch {
		case rc.AfterEntryID != "" && onPath[rc.AfterEntryID]:
			afterChange[rc.AfterEntryID] = append(afterChange[rc.AfterEntryID], rc)
		case rc.AfterEntryID == "":
			tailChange = append(tailChange, rc)
		}
	}
	// 提问记录与审批同规则:锚点在当前分支上按位置插入,空锚点补末尾,
	// 旧分支丢弃。
	afterQ := map[string][]Question{}
	var tailQ []Question
	for _, q := range meta.Questions {
		switch {
		case q.AfterEntryID != "" && onPath[q.AfterEntryID]:
			afterQ[q.AfterEntryID] = append(afterQ[q.AfterEntryID], q)
		case q.AfterEntryID == "":
			tailQ = append(tailQ, q)
		}
	}
	for _, entry := range path {
		if item := transcriptEntry(entry, meta); item != nil {
			out = append(out, item)
		}
		for _, esc := range afterEsc[entry.ID] {
			out = append(out, escalationEntry(esc))
		}
		for _, runErr := range afterErr[entry.ID] {
			out = append(out, runErrorEntry(runErr))
		}
		for _, rc := range afterChange[entry.ID] {
			out = append(out, runChangeEntry(rc))
		}
		for _, q := range afterQ[entry.ID] {
			out = append(out, questionEntry(q))
		}
	}
	for _, esc := range tailEsc {
		out = append(out, escalationEntry(esc))
	}
	for _, runErr := range tailErr {
		out = append(out, runErrorEntry(runErr))
	}
	for _, rc := range tailChange {
		out = append(out, runChangeEntry(rc))
	}
	for _, q := range tailQ {
		out = append(out, questionEntry(q))
	}
	return out, nil
}

// readPiEntries 解析 Pi 会话文件的全部条目(跳过文件头与非法行)。
// 最后一行没有换行符也能读到;空行、非 JSON、缺 type 的行全部跳过。
func readPiEntries(path string) ([]piEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var entries []piEntry
	for {
		line, err := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var entry piEntry
			if jsonErr := json.Unmarshal(trimmed, &entry); jsonErr == nil &&
				entry.Type != "" && entry.Type != "session" {
				entries = append(entries, entry)
			}
		}
		if err != nil {
			break
		}
	}
	return entries, nil
}

// activePath 取当前分支:叶子是文件里最后一个带 id 的条目,沿 parentId
// 回溯到根后反转。全部条目都没有 id 时(v1 线性格式)按文件顺序全取;
// parentId 指向不存在的条目(文件损坏)时当作根,上限迭代防环。
func activePath(entries []piEntry) []piEntry {
	if len(entries) == 0 {
		return nil
	}
	leaf := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].ID != "" {
			leaf = i
			break
		}
	}
	if leaf < 0 {
		return entries
	}
	byID := make(map[string]piEntry, len(entries))
	for _, entry := range entries {
		if entry.ID != "" {
			byID[entry.ID] = entry
		}
	}
	var path []piEntry
	cur := entries[leaf]
	for i := 0; i <= len(entries); i++ {
		path = append(path, cur)
		if cur.ParentID == nil || *cur.ParentID == "" {
			break
		}
		parent, ok := byID[*cur.ParentID]
		if !ok {
			break
		}
		cur = parent
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// transcriptEntry 把单个 Pi 条目转成前端条目;不产出内容的条目返回 nil。
func transcriptEntry(entry piEntry, meta Meta) map[string]any {
	switch entry.Type {
	case "message":
		if entry.Message == nil {
			return nil
		}
		return messageEntry(entry, meta)
	case "compaction":
		out := map[string]any{
			"type":    "compaction",
			"summary": entry.Summary,
		}
		if entry.Timestamp != "" {
			out["timestamp"] = entry.Timestamp
		}
		if entry.TokensBefore != nil {
			out["tokens_before"] = *entry.TokensBefore
		}
		// 会话文件条目通常只有 tokensBefore;有后验估值时带上
		if entry.TokensAfter != nil {
			out["tokens_after"] = *entry.TokensAfter
		} else if entry.EstimatedTokensAfter != nil {
			out["tokens_after"] = *entry.EstimatedTokensAfter
		}
		return out
	default:
		return nil // model_change/usage/custom 等不进 transcript
	}
}

// messageEntry 把 message 条目转成前端消息条目;system/bashExecution 等
// 角色返回 nil。
func messageEntry(entry piEntry, meta Meta) map[string]any {
	msg := entry.Message
	message := map[string]any{}
	switch msg.Role {
	case "user":
		message["role"] = "user"
		text, images := splitContent(msg.Content)
		bound := bindingFor(meta, entry.ID)
		// 展示文本优先取 meta 里保存的用户原文:发给 Pi 的内容可能拼进了
		// 引用块或文本附件正文,那些不属于用户气泡本身。
		if bound != nil && bound.Text != "" {
			text = bound.Text
		}
		message["content"] = text
		if len(images) > 0 {
			message["attachments"] = imageAttachments(images, bound)
		}
		if bound != nil && len(bound.References) > 0 {
			refs := make([]map[string]any, 0, len(bound.References))
			for _, ref := range bound.References {
				refs = append(refs, map[string]any{"session_id": ref.SessionID, "title": ref.Title})
			}
			message["references"] = refs
		}
	case "assistant":
		message["role"] = "assistant"
		text, thinking, calls := splitAssistant(msg.Content)
		message["content"] = text
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		if thinking != "" {
			message["thinking"] = thinking
		}
	case "toolResult":
		message["role"] = "tool"
		text, _ := splitContent(msg.Content)
		message["content"] = text
		message["tool_call_id"] = msg.ToolCallID
		message["name"] = msg.ToolName
		message["is_error"] = msg.IsError
		if images := toolImages(msg.Details); len(images) > 0 {
			message["images"] = images
		}
		if msg.ToolName == "edit" && !msg.IsError {
			if patch := toolDetailsPatch(msg.Details); patch != "" {
				message["details_patch"] = patch
			}
		}
	default:
		return nil
	}
	out := map[string]any{
		"type":       "message",
		"message_id": messageIDFor(meta, entry.ID),
		"message":    message,
	}
	if entry.Timestamp != "" {
		out["timestamp"] = entry.Timestamp
	}
	return out
}

// escalationEntry 把审批记录转成前端条目;时间用 Pi 条目同款毫秒精度 ISO。
// scope/auto 只在有意义时输出(会话内允许 / 自动放行),其余省略。
func escalationEntry(esc Escalation) map[string]any {
	var granted any // 未决为 null,与前端 granted?: boolean | null 对齐
	if esc.Granted != nil {
		granted = *esc.Granted
	}
	out := map[string]any{
		"type":           "escalation",
		"escalation_id":  esc.EscalationID,
		"tool_name":      esc.ToolName,
		"requested_path": esc.RequestedPath,
		"resolved_path":  esc.ResolvedPath,
		"granted":        granted,
		"timestamp":      esc.At.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if len(esc.RequestedCommand) > 0 {
		out["requested_command"] = esc.RequestedCommand
	}
	if esc.Scope != "" {
		out["scope"] = esc.Scope
	}
	if esc.Auto {
		out["auto"] = true
	}
	return out
}

// runErrorEntry 把 run 失败记录转成前端条目;时间与审批同款毫秒精度 ISO。
func runErrorEntry(runErr RunError) map[string]any {
	return map[string]any{
		"type":      "run_error",
		"message":   runErr.Message,
		"timestamp": runErr.At.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// runChangeEntry 把一轮文件变更转成前端条目。
func runChangeEntry(rc RunChange) map[string]any {
	files := make([]map[string]any, 0, len(rc.Files))
	for _, file := range rc.Files {
		item := map[string]any{
			"path": file.Path, "kind": file.Kind, "size": file.Size,
			"additions": file.Additions, "deletions": file.Deletions, "has_diff": file.HasDiff,
		}
		if file.Binary {
			item["binary"] = true
		}
		if file.Truncated {
			item["truncated"] = true
		}
		files = append(files, item)
	}
	out := map[string]any{
		"type": "run_changes", "turn_id": rc.TurnID, "run_ids": rc.RunIDs,
		"total": rc.Total, "files": files,
		"timestamp": rc.At.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if rc.Incomplete {
		out["incomplete"] = true
	}
	return out
}

// questionEntry 把提问记录转成前端条目;未回答时没有 answers 与各标记,
// 布尔字段仅为真时输出。
func questionEntry(q Question) map[string]any {
	out := map[string]any{
		"type":         "question",
		"question_id":  q.QuestionID,
		"tool_call_id": q.ToolCallID,
		"questions":    questionItems(q.Questions),
		"timestamp":    q.AskedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if q.AnsweredAt != nil {
		out["answers"] = questionAnswers(q.Answers)
		if q.Skipped {
			out["skipped"] = true
		}
		if q.Cancelled {
			out["cancelled"] = true
		}
		if q.Auto {
			out["auto"] = true
		}
	}
	return out
}

func questionItems(items []QuestionItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		options := make([]map[string]any, 0, len(item.Options))
		for _, opt := range item.Options {
			option := map[string]any{"label": opt.Label}
			if opt.Description != "" {
				option["description"] = opt.Description
			}
			options = append(options, option)
		}
		out = append(out, map[string]any{
			"header": item.Header, "question": item.Question,
			"multiSelect": item.MultiSelect, "options": options,
		})
	}
	return out
}

func questionAnswers(answers []QuestionAnswer) []map[string]any {
	out := make([]map[string]any, 0, len(answers))
	for _, answer := range answers {
		item := map[string]any{"selected": answer.Selected}
		if answer.Other != "" {
			item["other"] = answer.Other
		}
		out = append(out, item)
	}
	return out
}

// bindingFor 按 EntryID 反查 meta 里的用户消息(取最后一条绑定)。
func bindingFor(meta Meta, entryID string) *UserMessage {
	var found *UserMessage
	for i := range meta.Messages {
		if meta.Messages[i].EntryID == entryID {
			found = &meta.Messages[i]
		}
	}
	return found
}

func messageIDFor(meta Meta, entryID string) string {
	if bound := bindingFor(meta, entryID); bound != nil && bound.MessageID != "" {
		return bound.MessageID
	}
	return entryID
}

// splitContent 提取 user/toolResult 内容:文本块按 \n 连接,另收集图片块。
// content 为字符串时直接返回;解析失败按空处理。
func splitContent(raw json.RawMessage) (string, []piBlock) {
	blocks := parseBlocks(raw)
	var texts []string
	var images []piBlock
	for _, block := range blocks {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "image":
			images = append(images, block)
		}
	}
	return strings.Join(texts, "\n"), images
}

// splitAssistant 提取 assistant 内容:文本/思考块按 \n 连接,
// toolCall 块转成 {id,name,raw_arguments}。
func splitAssistant(raw json.RawMessage) (text, thinking string, calls []map[string]any) {
	var texts, thinks []string
	for _, block := range parseBlocks(raw) {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "thinking":
			thinks = append(thinks, block.Thinking)
		case "toolCall":
			calls = append(calls, map[string]any{
				"id":            block.ID,
				"name":          block.Name,
				"raw_arguments": rawArguments(block.Arguments),
			})
		}
	}
	return strings.Join(texts, "\n"), strings.Join(thinks, "\n"), calls
}

// parseBlocks 解析 content:字符串退化为单个文本块,数组按块解析,其余为空。
func parseBlocks(raw json.RawMessage) []piBlock {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return []piBlock{{Type: "text", Text: s}}
		}
		return nil
	}
	if trimmed[0] != '[' {
		return nil
	}
	var blocks []piBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil
	}
	return blocks
}

// rawArguments 把 toolCall.arguments 转成 JSON 字符串:对象/数组原样,
// 字符串直接用,缺失兜底 "{}"(与 PoC to-transcript.mjs 一致)。
func rawArguments(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "{}"
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return "{}"
	}
	return string(trimmed)
}

// toolDetailsPatch 提取 edit 结果的统一 diff,空值与超大 patch 不透传。
func toolDetailsPatch(details json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(details, &object) != nil {
		return ""
	}
	var patch string
	if json.Unmarshal(object["patch"], &patch) != nil || patch == "" || len(patch) > 200<<10 {
		return ""
	}
	return patch
}

// imageAttachments 把 user 消息里的图片块转成 attachments;名字优先用 meta
// 记录(按顺序对齐),没有则 image-N(N 从 0 起)。
func imageAttachments(images []piBlock, bound *UserMessage) []map[string]any {
	attachments := make([]map[string]any, 0, len(images))
	for i, img := range images {
		name := fmt.Sprintf("image-%d", i)
		if bound != nil && i < len(bound.Attachments) && bound.Attachments[i].Name != "" {
			name = bound.Attachments[i].Name
		}
		mimeType := img.MimeType
		attachments = append(attachments, map[string]any{
			"name":      name,
			"mime_type": mimeType,
			"data_url":  "data:" + mimeType + ";base64," + img.Data,
		})
	}
	return attachments
}
