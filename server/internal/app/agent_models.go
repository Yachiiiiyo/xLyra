package app

import (
	"context"

	"github.com/google/uuid"

	"xlyra/server/internal/agent"
	"xlyra/server/internal/agentllm"
	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/config"
	sitepkg "xlyra/server/internal/site"
	"xlyra/server/internal/store"
)

// agentModelSource 是装配可用模型目录所需的后端依赖。
// 站点或数据库缺失时目录为 nil,/agent/models 返回不可用。
type agentModelSource struct {
	sites *sitepkg.Service
	db    *store.Store
}

// agentModelCatalog 把站点服务、规范模型仓库与配置文件适配成只读目录。
func agentModelCatalog(sources []agentModelSource, confFile *config.ConfigFile) agent.ModelCatalog {
	if len(sources) == 0 || sources[0].sites == nil || sources[0].db == nil {
		return nil
	}
	return modelCatalog{sites: sources[0].sites, db: sources[0].db, confFile: confFile}
}

// modelCatalog 复用站点列表、站点模型与规范模型的现有取数路径,
// 范围策略与 /agent/settings 读同一份配置。
type modelCatalog struct {
	sites    *sitepkg.Service
	db       *store.Store
	confFile *config.ConfigFile
}

func (c modelCatalog) Sites(ctx context.Context) ([]store.Site, error) {
	return c.sites.List(ctx)
}

func (c modelCatalog) SiteModels(ctx context.Context) (map[uuid.UUID][]store.SiteModel, error) {
	return c.sites.ListAllModelsBySite(ctx)
}

func (c modelCatalog) CanonicalModels(ctx context.Context) ([]store.CanonicalModel, error) {
	return store.NewCanonicalModelRepository(c.db.DB()).ListAll(ctx)
}

func (c modelCatalog) Scope(context.Context) (agent.ModelScope, error) {
	sitePolicy, modelPolicy, siteIDs, siteModelIDs := agentproxy.AccessPolicyFromConfig(c.confFile)
	return agent.ModelScope{
		SitePolicy:          sitePolicy,
		ModelPolicy:         modelPolicy,
		AllowedSiteIDs:      siteIDs,
		AllowedSiteModelIDs: siteModelIDs,
	}, nil
}

func (c modelCatalog) DefaultModel(ctx context.Context) (string, error) {
	return agentllm.DefaultModel(ctx, c.db)
}
