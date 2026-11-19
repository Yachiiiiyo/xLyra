package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"xlyra/server/internal/gateway"
	"xlyra/server/internal/store"
)

// 非对话类别不进入可选模型,与 Web 的 NON_CHAT_CATEGORIES 一致。
var nonChatCategories = map[string]struct{}{
	"image":     {},
	"audio":     {},
	"embedding": {},
}

// ModelCatalog 是拼装「Agent 可用模型」所需的只读数据。
// 生产实现由 app 层用站点服务、规范模型仓库与配置文件适配。
type ModelCatalog interface {
	Sites(ctx context.Context) ([]store.Site, error)
	SiteModels(ctx context.Context) (map[uuid.UUID][]store.SiteModel, error)
	CanonicalModels(ctx context.Context) ([]store.CanonicalModel, error)
	// Scope 返回站点/模型范围策略。策略只取 allow_all 与 allow_list,
	// 其他值按 allow_all 处理。
	Scope(ctx context.Context) (ModelScope, error)
	// DefaultModel 返回 model-memory 的全局默认模型;没有则为空串。
	DefaultModel(ctx context.Context) (string, error)
}

// ModelScope 对应 /agent/settings 的站点与模型范围策略。
type ModelScope struct {
	SitePolicy          string
	ModelPolicy         string
	AllowedSiteIDs      []string
	AllowedSiteModelIDs []string
}

// agentModel 是 GET /agent/models 列表中的一项。
// Reasoning 无可用档位时省略,与 Web parseGatewayModelReasoning 的返回一致。
type agentModel struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Category    string            `json:"category"`
	Reasoning   *agentModelReason `json:"reasoning,omitempty"`
}

// agentModelReason 是 reasoning_effort 解析后的稳定结构:
// levels 为规范档位(按档位高低排序),default 不在档位内时省略。
type agentModelReason struct {
	Levels            []string `json:"levels"`
	Default           string   `json:"default,omitempty"`
	ThinkingMandatory bool     `json:"thinking_mandatory,omitempty"`
}

// agentModelsResponse 是 GET /agent/models 的 data 部分。
// DefaultModel 不在可用列表中时省略。
type agentModelsResponse struct {
	Models       []agentModel `json:"models"`
	DefaultModel string       `json:"default_model,omitempty"`
}

// canonicalModelRef 是拼装时用到的规范模型字段。
type canonicalModelRef struct {
	ModelKey string
	Category string
	Reason   *agentModelReason
}

// buildAgentModels 按 Web 工作区的过滤规则拼装可用模型:
// 只取启用站点上 status=active 的站点模型;allow_list 策略按站点 id /
// 站点模型 id 过滤;排除 image/audio/embedding;id 取
// model_key || upstream_model_name,同 id 保留先出现的一项,
// 最后按 id 不区分大小写排序。
func buildAgentModels(sites []store.Site, modelsBySite map[uuid.UUID][]store.SiteModel, canonical []store.CanonicalModel, scope ModelScope) []agentModel {
	canonicalByID := make(map[uuid.UUID]canonicalModelRef, len(canonical))
	for _, model := range canonical {
		canonicalByID[model.ID] = canonicalModelRef{
			ModelKey: model.ModelKey,
			Category: model.Category,
			Reason:   reasoningForModelKey(model.ModelKey),
		}
	}
	allowedSites := stringSet(scope.AllowedSiteIDs)
	allowedModels := stringSet(scope.AllowedSiteModelIDs)
	filterSites := scope.SitePolicy == "allow_list"
	filterModels := scope.ModelPolicy == "allow_list"

	seen := map[string]struct{}{}
	models := make([]agentModel, 0)
	for _, site := range sites {
		if !site.Enabled {
			continue
		}
		if _, ok := allowedSites[site.ID.String()]; filterSites && !ok {
			continue
		}
		for _, item := range modelsBySite[site.ID] {
			if item.Status != "active" {
				continue
			}
			if _, ok := allowedModels[item.ID.String()]; filterModels && !ok {
				continue
			}
			var ref canonicalModelRef
			if item.CanonicalID.Valid {
				ref = canonicalByID[item.CanonicalID.UUID]
			}
			category := ref.Category
			if _, skip := nonChatCategories[category]; skip {
				continue
			}
			id := strings.TrimSpace(ref.ModelKey)
			if id == "" {
				id = strings.TrimSpace(item.UpstreamName)
			}
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			if category == "" {
				category = "chat"
			}
			displayName := strings.TrimSpace(item.DisplayName)
			if displayName == "" {
				displayName = id
			}
			models = append(models, agentModel{
				ID:          id,
				DisplayName: displayName,
				Category:    category,
				Reasoning:   ref.Reason,
			})
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// reasoningForModelKey 把网关声明的 reasoning_effort 解析成对外结构。
// 规范档位、去重与排序沿用 gateway.ReasoningEffortSpecForModel;
// 与 Web 一致,不对外暴露 mode。
func reasoningForModelKey(modelKey string) *agentModelReason {
	spec := gateway.ReasoningEffortSpecForModel(modelKey)
	if spec == nil || len(spec.Levels) == 0 {
		return nil
	}
	return &agentModelReason{
		Levels:            spec.Levels,
		Default:           spec.Default,
		ThinkingMandatory: spec.ThinkingMandatory,
	}
}
