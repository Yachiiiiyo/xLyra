package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xlyra/server/internal/agent/capabilities"
)

// newCapsHandler 构造注入真实能力存储(t.TempDir)的 handler。
func newCapsHandler(t *testing.T) (*Handler, *capabilities.Store) {
	t.Helper()
	store := capabilities.NewStore(t.TempDir())
	handler, _, _ := newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.Capabilities = store })
	return handler, store
}

const skillDocMD = `---
name: pdf-tools
description: PDF 处理技能
license: MIT
metadata:
  version: "1.0"
---
# 用法
处理 PDF。
`

// skillBody 把 SKILL.md 内容编成 {"content":"..."} 请求体。
func skillBody(content string) string {
	encoded, _ := json.Marshal(map[string]string{"content": content})
	return string(encoded)
}

func putSkill(t *testing.T, handler *Handler, name, content string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/skills/"+name, skillBody(content))
}

func TestSkillsEndpointsCRUD(t *testing.T) {
	handler, _ := newCapsHandler(t)

	// 空列表:skills 是数组而非 null。
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data struct {
			Enabled bool `json:"enabled"`
			Skills  []struct {
				Name string `json:"name"`
			} `json:"skills"`
		} `json:"data"`
	}
	decodeBody(t, rec, &list)
	if !list.Data.Enabled || list.Data.Skills == nil || len(list.Data.Skills) != 0 {
		t.Fatalf("empty list = %+v", list.Data)
	}

	// 创建技能。
	rec = putSkill(t, handler, "pdf-tools", skillDocMD)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, body %s", rec.Code, rec.Body.String())
	}
	var put struct {
		Data struct {
			Name    string `json:"name"`
			Changed bool   `json:"changed"`
		} `json:"data"`
	}
	decodeBody(t, rec, &put)
	if put.Data.Name != "pdf-tools" || !put.Data.Changed {
		t.Fatalf("put data = %+v", put.Data)
	}

	// 内容未变时 changed=false。
	rec = putSkill(t, handler, "pdf-tools", skillDocMD)
	decodeBody(t, rec, &put)
	if rec.Code != http.StatusOK || put.Data.Changed {
		t.Fatalf("unchanged put = %d %+v", rec.Code, put.Data)
	}

	// 详情。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/pdf-tools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Data struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Scope       string            `json:"scope"`
			Enabled     bool              `json:"enabled"`
			Content     string            `json:"content"`
			Metadata    map[string]string `json:"metadata"`
			Resources   []string          `json:"resources"`
		} `json:"data"`
	}
	decodeBody(t, rec, &detail)
	if detail.Data.Name != "pdf-tools" || detail.Data.Description != "PDF 处理技能" ||
		detail.Data.Scope != "managed" || !detail.Data.Enabled || detail.Data.Content != skillDocMD ||
		detail.Data.Metadata["version"] != "1.0" || detail.Data.Resources == nil {
		t.Fatalf("detail = %+v", detail.Data)
	}

	// 列表含新技能。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills", "")
	decodeBody(t, rec, &list)
	if len(list.Data.Skills) != 1 || list.Data.Skills[0].Name != "pdf-tools" {
		t.Fatalf("list after put = %+v", list.Data)
	}

	// 删除:前端会带 {} 请求体。
	rec = doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/skills/pdf-tools", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body %s", rec.Code, rec.Body.String())
	}
	var deleted struct {
		Data struct {
			Name    string `json:"name"`
			Deleted bool   `json:"deleted"`
		} `json:"data"`
	}
	decodeBody(t, rec, &deleted)
	if deleted.Data.Name != "pdf-tools" || !deleted.Data.Deleted {
		t.Fatalf("delete data = %+v", deleted.Data)
	}
	// 再删(幂等,空请求体)返回 deleted=false。
	rec = doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/skills/pdf-tools", "")
	decodeBody(t, rec, &deleted)
	if rec.Code != http.StatusOK || deleted.Data.Deleted {
		t.Fatalf("idempotent delete = %d %+v", rec.Code, deleted.Data)
	}
}

func TestSkillsEndpointsErrors(t *testing.T) {
	handler, _ := newCapsHandler(t)

	// 不存在 → 404 not_found。
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/ghost", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing skill status = %d, body %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "not_found" {
		t.Fatalf("missing skill code = %s", code)
	}

	// 非法技能名 → 400 invalid_request(中文说明)。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/Bad_Name", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name status = %d, body %s", rec.Code, rec.Body.String())
	}
	if code, message := errorBody(t, rec); code != "invalid_request" || !strings.Contains(message, "非法技能名") {
		t.Fatalf("bad name = %s %q", code, message)
	}

	// 内容缺 frontmatter → 400。
	rec = putSkill(t, handler, "broken", "no frontmatter")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad content status = %d, body %s", rec.Code, rec.Body.String())
	}

	// 请求体非法 JSON → 400。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/skills/broken", "not-json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body status = %d", rec.Code)
	}

	// 资源文件读取:先建技能再读 SKILL.md。
	rec = putSkill(t, handler, "pdf-tools", skillDocMD)
	if rec.Code != http.StatusOK {
		t.Fatalf("put skill status = %d", rec.Code)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/pdf-tools/file?path=SKILL.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("skill file status = %d, body %s", rec.Code, rec.Body.String())
	}
	var file struct {
		Data struct {
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		} `json:"data"`
	}
	decodeBody(t, rec, &file)
	if file.Data.Content != skillDocMD || file.Data.Truncated {
		t.Fatalf("skill file = %+v", file.Data)
	}

	// 缺 path 参数 → 400;不存在的文件 → 404;越界路径 → 400。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/pdf-tools/file", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing path status = %d", rec.Code)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/pdf-tools/file?path=missing.txt", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file status = %d", rec.Code)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/pdf-tools/file?path=../secrets", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("escaping path status = %d", rec.Code)
	}
	// 不存在的技能读文件 → 404。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills/ghost/file?path=a.txt", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ghost skill file status = %d", rec.Code)
	}
}

func TestSkillsBodyLimit(t *testing.T) {
	handler, _ := newCapsHandler(t)

	// PUT skills 上限 1MB:超过 → 413。
	huge := strings.Repeat("a", skillBodyLimit+1)
	rec := doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/skills/big", `{"content":"`+huge+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized skill status = %d, body %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "request_body_too_large" {
		t.Fatalf("oversized skill code = %s", code)
	}

	// 其余接口上限 256KB:config PUT 超过 → 413。
	bigList := strings.Repeat("\"x\",", 64*1024)
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"agent":{"disabled_skills":[`+strings.TrimSuffix(bigList, ",")+`]}}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized config status = %d, body %s", rec.Code, rec.Body.String())
	}

	// workspace PUT 同样 256KB。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/workspace/file",
		`{"path":"AGENTS.md","content":"`+strings.Repeat("a", configBodyLimit)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized workspace status = %d", rec.Code)
	}
}

func TestWorkspaceFileEndpoints(t *testing.T) {
	handler, _ := newCapsHandler(t)

	// 文件不存在 → 404(前端把 404 当不存在)。
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/workspace/file?path=AGENTS.md", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file status = %d, body %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "not_found" {
		t.Fatalf("missing file code = %s", code)
	}

	// 写入 → success。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/workspace/file",
		`{"path":"AGENTS.md","content":"# 人格\n保持简洁。"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, body %s", rec.Code, rec.Body.String())
	}
	var success struct {
		Success bool `json:"success"`
	}
	decodeBody(t, rec, &success)
	if !success.Success {
		t.Fatalf("put body = %s", rec.Body.String())
	}

	// 读回。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/workspace/file?path=AGENTS.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var payload struct {
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.Content != "# 人格\n保持简洁。" {
		t.Fatalf("content = %q", payload.Data.Content)
	}

	// 非 AGENTS.md:GET 404、PUT/DELETE 400(中文说明)。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/workspace/file?path=CLAUDE.md", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other path get status = %d", rec.Code)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/agent/workspace/file", `{"path":"CLAUDE.md","content":"x"}`},
		{http.MethodDelete, "/api/v1/agent/workspace/file?path=CLAUDE.md", ""},
		{http.MethodDelete, "/api/v1/agent/workspace/file?path=", ""},
	} {
		rec = doJSONAgent(t, handler, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: status = %d", tc.method, tc.path, rec.Code)
		}
		if code, message := errorBody(t, rec); code != "invalid_request" || !strings.Contains(message, "内置 Agent 仅支持 AGENTS.md") {
			t.Fatalf("%s %s: %s %q", tc.method, tc.path, code, message)
		}
	}

	// 超过 64KB → 400(存储层校验)。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/workspace/file",
		`{"path":"AGENTS.md","content":"`+strings.Repeat("a", 65*1024)+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized content status = %d", rec.Code)
	}

	// 删除 → success,再读 404。
	rec = doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/workspace/file?path=AGENTS.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	decodeBody(t, rec, &success)
	if !success.Success {
		t.Fatalf("delete body = %s", rec.Body.String())
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/workspace/file?path=AGENTS.md", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d", rec.Code)
	}
}

func TestConfigEndpoints(t *testing.T) {
	handler, _ := newCapsHandler(t)

	// 默认配置:context_window 未设置时省略。
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "context_window") {
		t.Fatalf("unset context_window should be omitted: %s", rec.Body.String())
	}
	var payload struct {
		Data struct {
			Endpoints []any               `json:"endpoints"`
			Agent     capabilities.Config `json:"agent"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.Endpoints == nil || len(payload.Data.Endpoints) != 0 {
		t.Fatalf("endpoints = %v, want []", payload.Data.Endpoints)
	}
	if !payload.Data.Agent.EnableAgentsMD || !payload.Data.Agent.EnableSkills ||
		payload.Data.Agent.CompactTriggerRatio != 0.9 || payload.Data.Agent.ContextWindow != 0 {
		t.Fatalf("default agent = %+v", payload.Data.Agent)
	}

	// 先放两个技能并停用一个,验证后续合并不丢 disabled_skills。
	for name, doc := range map[string]string{
		"pdf-tools":  "---\nname: pdf-tools\ndescription: PDF\n---\nx",
		"web-search": "---\nname: web-search\ndescription: 搜索\n---\ny",
	} {
		if rec := putSkill(t, handler, name, doc); rec.Code != http.StatusOK {
			t.Fatalf("put skill %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"endpoints":[{"name":"legacy"}],"agent":{"disabled_skills":["web-search"]},"server":{"x":1}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable skill status = %d, body %s", rec.Code, rec.Body.String())
	}

	// 只改 ratio:其余字段(含 disabled_skills)保持原值;endpoints/server 被忽略。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"endpoints":[],"agent":{"compact_trigger_ratio":0.8}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge ratio status = %d, body %s", rec.Code, rec.Body.String())
	}
	var success struct {
		Success bool `json:"success"`
	}
	decodeBody(t, rec, &success)
	if !success.Success {
		t.Fatalf("merge body = %s", rec.Body.String())
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/config", "")
	decodeBody(t, rec, &payload)
	if payload.Data.Agent.CompactTriggerRatio != 0.8 || !payload.Data.Agent.EnableSkills || !payload.Data.Agent.EnableAgentsMD ||
		len(payload.Data.Agent.DisabledSkills) != 1 || payload.Data.Agent.DisabledSkills[0] != "web-search" {
		t.Fatalf("merged agent = %+v", payload.Data.Agent)
	}
	// 列表里被停用的技能 enabled=false。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/skills", "")
	var list struct {
		Data struct {
			Skills []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"skills"`
		} `json:"data"`
	}
	decodeBody(t, rec, &list)
	enabled := map[string]bool{}
	for _, skill := range list.Data.Skills {
		enabled[skill.Name] = skill.Enabled
	}
	if !enabled["pdf-tools"] || enabled["web-search"] {
		t.Fatalf("skills enabled = %v", enabled)
	}

	// 设置与清除 context_window:0 表示回退(响应里省略)。
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"agent":{"context_window":500000}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set window status = %d", rec.Code)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/config", "")
	decodeBody(t, rec, &payload)
	if payload.Data.Agent.ContextWindow != 500000 {
		t.Fatalf("context_window = %d, want 500000", payload.Data.Agent.ContextWindow)
	}
	rec = doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"agent":{"context_window":0}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear window status = %d, body %s", rec.Code, rec.Body.String())
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/config", "")
	if strings.Contains(rec.Body.String(), "context_window") {
		t.Fatalf("cleared context_window should be omitted: %s", rec.Body.String())
	}
}

func TestConfigInvalidValues(t *testing.T) {
	handler, _ := newCapsHandler(t)

	for name, body := range map[string]string{
		"ratio out of range":  `{"agent":{"compact_trigger_ratio":0.3}}`,
		"ratio not number":    `{"agent":{"compact_trigger_ratio":"0.8"}}`,
		"window out of range": `{"agent":{"context_window":100}}`,
		"window not integer":  `{"agent":{"context_window":12.5}}`,
		"bad disabled skill":  `{"agent":{"disabled_skills":["不是技能名"]}}`,
		"disabled not array":  `{"agent":{"disabled_skills":"pdf-tools"}}`,
		"agents_md not bool":  `{"agent":{"enable_agents_md":"yes"}}`,
		"skills not bool":     `{"agent":{"enable_skills":1}}`,
		"agent not object":    `{"agent":[1,2]}`,
		"garbage body":        "not-json",
	} {
		rec := doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body %s", name, rec.Code, rec.Body.String())
		}
		if code, message := errorBody(t, rec); code != "invalid_request" || message == "" {
			t.Fatalf("%s: code = %s, message = %q", name, code, message)
		}
	}

	// 合法值仍可通过。
	rec := doJSONAgent(t, handler, http.MethodPut, "/api/v1/agent/config",
		`{"agent":{"enable_skills":true,"enable_agents_md":false,"compact_trigger_ratio":0.95}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid config status = %d, body %s", rec.Code, rec.Body.String())
	}
}
