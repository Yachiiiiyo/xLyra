package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"xlyra/server/internal/admin"
	"xlyra/server/internal/agentllm"
	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/analytics"
	"xlyra/server/internal/auth"
	"xlyra/server/internal/catalog"
	"xlyra/server/internal/cloudclient"
	"xlyra/server/internal/config"
	"xlyra/server/internal/dashboard"
	"xlyra/server/internal/downloads"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/health"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/httpx"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/newapi"
	oauthsvc "xlyra/server/internal/oauth"
	"xlyra/server/internal/observability"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/settings"
	"xlyra/server/internal/site"
	"xlyra/server/internal/store"
	"xlyra/server/internal/systemstats"
	"xlyra/server/internal/usage"
	"xlyra/server/internal/version"
)

func parseAgentUUIDs(values []string) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err == nil && id != uuid.Nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func NewRouter(cfg config.Config, logger *slog.Logger, db *store.Store, confFile *config.ConfigFile, masterKey string) http.Handler {
	router, _ := NewRouterWithGateway(cfg, logger, db, confFile, masterKey)
	return router
}

func NewRouterWithGateway(cfg config.Config, logger *slog.Logger, db *store.Store, confFile *config.ConfigFile, masterKey string) (http.Handler, *gateway.Handler) {
	handler, gatewayHandler, _ := NewRouterWithGatewayWithOAuth(context.Background(), nil, cfg, logger, db, confFile, masterKey, nil, nil)
	return handler, gatewayHandler
}

func NewRouterWithGatewayWithOAuth(ctx context.Context, tasks *sync.WaitGroup, cfg config.Config, logger *slog.Logger, db *store.Store, confFile *config.ConfigFile, masterKey string, sharedOAuth *oauthsvc.Service, sharedSite *site.Service) (http.Handler, *gateway.Handler, *market.Service) {
	appTimeZone := config.ResolveTimeZone()
	var authService *auth.Service
	if db != nil {
		authService = auth.NewService(db.DB(), masterKey, confFile)
		// 设备令牌闲置失效天数来自环境变量 DEVICE_IDLE_DAYS（0 表示不限）。
		authService.SetDeviceIdleDays(cfg.DeviceIdleDays)
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := authService.WarmBootstrapCache(warmCtx); err != nil {
			logger.Warn("bootstrap admin cache warmup failed", "error", err)
		} else {
			logger.Info("bootstrap admin cache ready")
		}
		warmCancel()
	}

	httpClients := httpclient.NewManager(confFile)
	defaultHTTPClient, _ := httpClients.Client(httpclient.DefaultProfile())
	newAPIService := newapi.NewServiceWithHTTPClient(defaultHTTPClient)
	var oauthService *oauthsvc.Service
	var siteService *site.Service
	var catalogService *catalog.Service
	var dashboardService *dashboard.Service
	var analyticsService *analytics.Service
	var routerService *routeengine.Service
	var usageService *usage.Service
	var jsPluginManager *jsplugin.Manager
	var pluginMarket *market.Service
	var cloudClient *cloudclient.Client
	var deviceRepo store.AdminDeviceRepository
	if db != nil {
		deviceRepo = store.NewAdminDeviceRepository(db.DB())
		jsPluginManager = jsplugin.NewManager(db, jsplugin.DefaultCatalog())
		marketService, err := market.NewService(store.NewJSPluginRepository(db.DB()), httpClients, confFile, nil, nil)
		if err != nil {
			logger.Warn("plugin market service disabled", "error", err)
		} else {
			pluginMarket = marketService
			jsPluginManager.SetMarketTrust(pluginMarket)
			jsPluginManager.SetAutoUpdateRollback(pluginMarket)
			pluginMarket.SetRuntime(jsPluginManager)
			pluginMarket.SetAuditor(authService)
		}
		reloadCtx, reloadCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := jsPluginManager.Reload(reloadCtx); err != nil {
			logger.Warn("js plugin catalog reload failed", "error", err)
		}
		reloadCancel()
		jsPluginManager.StartSync(ctx, 5*time.Second)
		cloudClient = cloudclient.New(
			store.NewCloudBindingRepository(db.DB()),
			store.NewJSPluginRepository(db.DB()),
			masterKey,
			func(ctx context.Context) (string, error) {
				if pluginMarket == nil {
					return "", errors.New("未缓存已验签的市场 root")
				}
				return pluginMarket.CloudURL(ctx)
			},
			cloudclient.Options{
				Logger:  logger,
				Devices: devicePushStore{repo: deviceRepo},
				Audit: func(ctx context.Context, action string, metadata map[string]any) {
					if authService == nil {
						return
					}
					authService.RecordAudit(ctx, auth.AuditInput{
						Action:       action,
						ResourceType: "cloud_binding",
						Success:      true,
						Metadata:     metadata,
					})
				},
			},
		)
		if tasks != nil {
			tasks.Add(1)
		}
		go func() {
			if tasks != nil {
				defer tasks.Done()
			}
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("xLyra Cloud binding task stopped", "error", rec)
				}
			}()
			cloudClient.Run(ctx)
		}()
		if tasks != nil {
			if done := jsPluginManager.CatalogSyncDone(); done != nil {
				tasks.Add(1)
				go func() {
					defer tasks.Done()
					<-done
				}()
			}
		}
		oauthService = sharedOAuth
		if oauthService == nil {
			oauthService = oauthsvc.NewService(db, masterKey, confFile)
		}
		wireJSPluginAutomation(oauthService, jsPluginManager, authService, defaultHTTPClient)
		// sharedSite is the instance background jobs already hold. Plugin modules
		// have to land on it; a second service never sees them.
		siteService = sharedSite
		if siteService == nil {
			siteService = site.NewServiceWithOAuthService(db, masterKey, appTimeZone, oauthService, confFile)
		}
		siteProviderCurrent = func() *jsplugin.Registry {
			return jsPluginManager.Catalog().Registry()
		}
		jsPluginManager.SetCatalogListener(func(registry *jsplugin.Registry) {
			applySiteProviders(registry, siteService, logger)
		})
		applySiteProviders(jsPluginManager.Catalog().Registry(), siteService, logger)
		config.RegisterGeneralValidator(func(cfg config.GeneralConfig) error {
			return site.ValidateJSPluginRuntime()
		})
		catalogService = catalog.NewService(db, confFile)
		dashboardService = dashboard.NewService(db, appTimeZone)
		analyticsService = analytics.NewService(db, appTimeZone)
		routerService = routeengine.NewService(db)
		routerService.SetSiteTypeFilter(site.SiteTypeAvailable)
		usageService = usage.NewService(db, appTimeZone)
	}
	systemStatsService := systemstats.NewService(appTimeZone)
	downloadService := downloads.NewService()
	gatewayHandler := gateway.NewHandlerWithOAuthService(logger.With("thread", "gateway"), authService, routerService, db, masterKey, appTimeZone, oauthService, confFile)
	if siteService != nil {
		gatewayHandler = gatewayHandler.WithPluginAccountRefresher(pluginAccountRefresher{sites: siteService})
	}
	agentLLMHandler := agentllm.NewHandler(logger.With("thread", "agent-llm"), db, gatewayHandler)
	agentLLMHandler.SetImageModelsSource(func() []agentproxy.ImageModelSelection {
		return agentproxy.ImageModelsFromConfig(confFile)
	})
	agentLLMHandler.SetImageGenerationOptions(cfg.AgentImageMaxCalls, cfg.AgentImageTimeout)
	agentProxyHandler := agentproxy.NewHandler(logger.With("thread", "agent-proxy"), config.ResolveWorkdir())
	syncAgentAccessPolicy := func() {
		if db == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sitePolicy, modelPolicy, siteIDs, siteModelIDs := agentproxy.AccessPolicyFromConfig(confFile)
		if err := agentLLMHandler.SyncAccessPolicy(ctx, agentllm.AccessPolicy{SitePolicy: sitePolicy, ModelPolicy: modelPolicy, SiteIDs: siteIDs, SiteModelIDs: siteModelIDs}); err != nil {
			logger.Warn("failed to sync agent access policy", "error", err)
		}
	}
	agentProxyHandler.SetOnSettingsUpdated(syncAgentAccessPolicy)
	if db != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			gatewayHandler.PrewarmModelsCache(ctx)
		}()
		go syncAgentAccessPolicy()
	}
	// 内置 Pi 是唯一引擎，始终装配。
	builtin := newBuiltinAgentWithDevices(cfg, logger.With("thread", "agent-builtin"), confFile, routerService, agentLLMHandler, agentLLMHandler.ModelMemory, deviceRepo, agentModelSource{sites: siteService, db: db})
	builtin.startPush(ctx, tasks, cloudClient)
	if tasks != nil {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			builtin.shutdown(shutdownCtx)
		}()
	}
	adminHandler := admin.NewHandler(logger.With("thread", "admin"), authService, siteService, catalogService, routerService, usageService, dashboardService, systemStatsService, &gatewayHandler, newAPIService, oauthService, appTimeZone).WithDownloadService(downloadService).WithTrafficFlowStore(db).WithAnalyticsService(analyticsService)
	if jsPluginManager != nil {
		adminHandler = adminHandler.WithJSPlugins(jsPluginManager)
	}
	if pluginMarket != nil {
		adminHandler = adminHandler.WithPluginMarket(pluginMarket)
	}
	if cloudClient != nil {
		adminHandler = adminHandler.WithCloudClient(cloudClient)
		adminHandler.PushSyncWaker = cloudClient
		adminHandler.PushSender = cloudClient
		adminHandler.HubStatus = func(ctx context.Context) (bool, string) {
			status, err := cloudClient.Status(ctx)
			if err != nil {
				return false, err.Error()
			}
			return status.Status == store.CloudStatusBound, status.LastError
		}
	}
	healthHandler := health.NewHandler(cfg, db)
	var preRestore, postRestore, databaseRestored func(context.Context) error
	if builtin.beginRestore != nil {
		// 恢复前先拦住 Agent 运行(有进行中的任务则拒绝恢复);
		// 恢复后(含失败收敛)按相反顺序放行。
		preRestore = func(ctx context.Context) error {
			return builtin.beginRestore()
		}
		postRestore = func(ctx context.Context) error {
			// 恢复出的工作区文件属主是 xLyra 进程用户;沙箱开启时放开权限。
			if builtin.relaxWorkspace != nil {
				builtin.relaxWorkspace()
			}
			builtin.endRestore()
			return nil
		}
	}
	if authService != nil || jsPluginManager != nil {
		databaseRestored = func(ctx context.Context) error {
			if authService != nil {
				if err := authService.RefreshBootstrapCache(ctx); err != nil {
					authService.ClearBootstrapCache()
					logger.Warn("bootstrap admin cache refresh after restore failed", "error", err)
				}
			}
			// 插件类别恢复后,运行时需要按库里的新内容重新加载。
			if jsPluginManager != nil {
				if err := jsPluginManager.Reload(ctx); err != nil {
					logger.Warn("js plugin reload after restore failed", "error", err)
				}
			}
			return nil
		}
	}
	settingsHandler := settings.NewHandlerWithBackup(logger.With("thread", "settings"), confFile, db, masterKey, downloadService, resolveAgentDataDir(cfg), resolveAgentWorkspaceDir(cfg, resolveAgentDataDir(cfg)), preRestore, postRestore, databaseRestored, appTimeZone)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(httpx.WithRequestIDHeader)
	r.Use(httpx.RealClientIP(cfg.TrustedProxies))
	r.Use(httpx.IPWhitelist(confFile))
	r.Use(middleware.Recoverer)
	r.Use(routeAwareTimeout(cfg.RequestTimeout))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-API-Key", "X-Access-Token", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(observability.RequestLogger(logger))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "route not found")
	})
	if cfg.StaticDir != "" {
		r.Handle("/*", newSPAHandler(cfg.StaticDir, authService))
	}
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	r.Get("/healthz", healthHandler.Healthz)
	r.Get("/readyz", healthHandler.Readyz)
	r.Get("/debug/stats", healthHandler.Stats)

	r.Route("/api", func(api chi.Router) {
		api.Route("/v1", func(v1 chi.Router) {
			v1.Get("/bootstrap/status", adminHandler.BootstrapStatus)
			v1.Get("/auth/state", adminHandler.AuthState)
			v1.Post("/bootstrap/register", adminHandler.BootstrapRegister)
			v1.Post("/auth/session", adminHandler.CreateSession)
			// App 扫码后的配对兑换: 无需登录, 按 IP 限速。
			v1.Post("/devices/pair", adminHandler.RedeemDevicePairing)
			v1.Get("/system/version", func(w http.ResponseWriter, _ *http.Request) {
				httpx.JSON(w, http.StatusOK, version.Current())
			})
			v1.Get("/oauth/providers/codex/callback", adminHandler.CodexOAuthCallback)
			v1.Get("/oauth/providers/antigravity/callback", adminHandler.AntigravityOAuthCallback)
			v1.Get("/site-types", adminHandler.ListSiteTypes)

			v1.Group(func(protected chi.Router) {
				protected.Use(requireAdmin(authService))
				protected.Use(requireAdminCSRF(authService))
				protected.Use(adminHandler.AuditAdminMutation)
				protected.Route("/agent", func(agentRouter chi.Router) {
					registerAgentRoutes(agentRouter, cfg, confFile, agentProxyHandler, builtin)
				})
				protected.Get("/auth/session", adminHandler.CurrentSession)
				protected.Delete("/auth/session", adminHandler.DeleteSession)
				registerAdminProfileRoutes(protected, adminHandler)
				registerAdminDeviceRoutes(protected, adminHandler)
				protected.Get("/profile", adminHandler.GetProfile)
				protected.Get("/profile/sessions", adminHandler.ListProfileSessions)
				protected.Delete("/profile/sessions", adminHandler.DeleteOtherProfileSessions)
				protected.Delete("/profile/sessions/{sessionID}", adminHandler.DeleteProfileSession)
				protected.Get("/audit-logs", adminHandler.ListAuditLogs)
				protected.Get("/js-plugin-trusted-keys", adminHandler.ListJSPluginTrustedKeys)
				protected.Post("/js-plugin-trusted-keys", adminHandler.CreateJSPluginTrustedKey)
				protected.Delete("/js-plugin-trusted-keys/{id}", adminHandler.DeleteJSPluginTrustedKey)
				protected.Get("/plugin-icons/{pluginID}", adminHandler.GetPluginIcon)
				protected.Get("/js-plugins", adminHandler.ListJSPlugins)
				protected.Post("/js-plugins", adminHandler.UploadJSPlugin)
				protected.Get("/js-plugins/{id}", adminHandler.GetJSPlugin)
				protected.Put("/js-plugins/{id}/subscription", adminHandler.UpdateJSPluginSubscription)
				protected.Post("/js-plugins/{id}/versions/{version}/enable", adminHandler.EnableJSPluginVersion)
				protected.Post("/js-plugins/{id}/disable", adminHandler.DisableJSPlugin)
				protected.Delete("/js-plugins/{id}", adminHandler.DeleteJSPlugin)
				protected.Delete("/js-plugins/{id}/versions/{version}", adminHandler.DeleteJSPluginVersion)
				protected.Post("/js-plugins/{id}/versions/{version}/bind-protocol", adminHandler.BindJSPluginProtocolSlug)
				protected.Post("/js-plugins/{id}/try", adminHandler.TryJSPlugin)
				protected.Post("/js-plugins/{id}/versions/{version}/bind-site", adminHandler.BindJSPluginSite)
				protected.Post("/js-plugins/{id}/unbind-site", adminHandler.UnbindJSPluginSite)
				protected.Post("/js-plugins/{id}/preview-pricing", adminHandler.PreviewJSPluginPricing)
				protected.Get("/js-plugins/{id}/automations", adminHandler.ListJSPluginAutomations)
				protected.Post("/js-plugins/{id}/automations", adminHandler.CreateJSPluginAutomation)
				protected.Put("/js-plugins/{id}/automations/{binding_id}", adminHandler.UpdateJSPluginAutomation)
				protected.Delete("/js-plugins/{id}/automations/{binding_id}", adminHandler.DeleteJSPluginAutomation)
				protected.Get("/js-plugins/{id}/inputs/{name}/options", adminHandler.ListJSPluginAutomationOptions)
				protected.Get("/js-plugins/{id}/action-log", adminHandler.ListJSPluginActionLog)
				admin.RegisterPluginMarket(protected, adminHandler)
				admin.RegisterCloudBinding(protected, adminHandler, cloudClient)
				protected.Get("/downloads/{downloadID}", downloadService.Download)
				protected.Get("/dashboard/usage", adminHandler.DashboardUsage)
				protected.Get("/dashboard/cooldowns", adminHandler.DashboardCooldowns)
				protected.Get("/dashboard/health", adminHandler.DashboardHealth)
				protected.Get("/dashboard/insights", adminHandler.DashboardInsights)
				protected.Get("/dashboard/epaper-summary", adminHandler.DashboardEpaperSummary)
				protected.Get("/dashboard/resources/stream", adminHandler.DashboardResourceStream)
				protected.Route("/analytics", func(analyticsRouter chi.Router) {
					analyticsRouter.Use(middleware.Compress(5, "application/json"))
					analyticsRouter.Get("/usage", adminHandler.AnalyticsUsage)
					analyticsRouter.Get("/options", adminHandler.AnalyticsOptions)
					analyticsRouter.Get("/api-key-contributions", adminHandler.AnalyticsAPIKeyContributions)
					analyticsRouter.Get("/dataset", adminHandler.AnalyticsDataset)
				})
				protected.Get("/traffic-flow/topology", adminHandler.TrafficFlowTopology)
				protected.Get("/traffic-flow/stream", adminHandler.TrafficFlowStream)
				protected.Get("/settings/site-groups", adminHandler.ListSiteGroups)
				protected.Post("/settings/site-groups", adminHandler.CreateSiteGroup)
				protected.Get("/settings/site-groups/{siteGroupID}", adminHandler.GetSiteGroup)
				protected.Put("/settings/site-groups/{siteGroupID}", adminHandler.UpdateSiteGroup)
				protected.Delete("/settings/site-groups/{siteGroupID}", adminHandler.DeleteSiteGroup)
				protected.Get("/api-keys", adminHandler.ListAPIKeys)
				protected.Post("/api-keys", adminHandler.CreateAPIKey)
				protected.Put("/api-keys/order", adminHandler.ReorderAPIKeys)
				protected.Get("/api-keys/{apiKeyID}", adminHandler.GetAPIKey)
				protected.Get("/api-keys/{apiKeyID}/reveal", adminHandler.RevealAPIKey)
				protected.Post("/api-keys/{apiKeyID}/rotate", adminHandler.RotateAPIKey)
				protected.Put("/api-keys/{apiKeyID}", adminHandler.UpdateAPIKey)
				protected.Post("/api-keys/{apiKeyID}/quota/increase", adminHandler.IncreaseAPIKeyQuota)
				protected.Post("/api-keys/{apiKeyID}/quota/reset", adminHandler.ResetAPIKeyQuota)
				protected.Delete("/api-keys/{apiKeyID}", adminHandler.DeleteAPIKey)
				protected.Get("/api-keys/{apiKeyID}/site-models", adminHandler.ListAPIKeySiteModels)
				protected.Put("/api-keys/{apiKeyID}/site-models", adminHandler.UpdateAPIKeySiteModels)
				protected.Get("/api-keys/{apiKeyID}/sites", adminHandler.ListAPIKeySites)
				protected.Put("/api-keys/{apiKeyID}/sites", adminHandler.UpdateAPIKeySites)
				protected.Get("/api-keys/{apiKeyID}/site-groups", adminHandler.ListAPIKeySiteGroups)
				protected.Put("/api-keys/{apiKeyID}/site-groups", adminHandler.UpdateAPIKeySiteGroups)
				protected.Put("/api-keys/{apiKeyID}/model-mappings", adminHandler.UpdateAPIKeyModelMappings)
				protected.Post("/api-keys/{apiKeyID}/model-check", adminHandler.CheckAPIKeyModel)
				protected.Get("/requests", adminHandler.ListRequestLogs)
				protected.Get("/requests/summary", adminHandler.RequestLogSummary)
				protected.Get("/requests/channel-split", adminHandler.RequestChannelSplit)
				protected.Get("/requests/{requestLogID}", adminHandler.GetRequestLog)
				protected.Post("/site-types/detect", adminHandler.DetectSiteType)
				protected.Get("/sites", adminHandler.ListSites)
				protected.Post("/sites", adminHandler.CreateSite)
				protected.Post("/sites/refresh", adminHandler.RefreshAllSites)
				protected.Get("/sites/{siteID}", adminHandler.GetSite)
				protected.Put("/sites/{siteID}", adminHandler.UpdateSite)
				protected.Put("/sites/{siteID}/enabled", adminHandler.UpdateSiteEnabled)
				protected.Delete("/sites/{siteID}", adminHandler.DeleteSite)
				protected.Post("/sites/{siteID}/refresh", adminHandler.RefreshSite)
				protected.Post("/sites/{siteID}/validate", adminHandler.ValidateSite)
				protected.Get("/sites/{siteID}/health", adminHandler.GetSiteHealth)
				protected.Get("/sites/{siteID}/health/history", adminHandler.GetSiteHealthHistory)
				protected.Get("/sites/{siteID}/health/hourly", adminHandler.GetSiteHealthHourly)
				protected.Post("/sites/{siteID}/health-check", adminHandler.CheckSiteHealth)
				protected.Get("/sites/{siteID}/api-keys", adminHandler.ListSiteAPIKeys)
				protected.Get("/sites/{siteID}/api-keys/{apiKeyID}/reveal", adminHandler.RevealSiteAPIKey)
				protected.Post("/sites/{siteID}/api-keys", adminHandler.CreateSiteAPIKey)
				protected.Put("/sites/{siteID}/api-keys/{apiKeyID}", adminHandler.UpdateSiteAPIKey)
				protected.Delete("/sites/{siteID}/api-keys/{apiKeyID}", adminHandler.DeleteSiteAPIKey)
				protected.Put("/sites/{siteID}/api-keys/{apiKeyID}/secret", adminHandler.UpdateSiteAPIKeySecret)
				protected.Put("/sites/{siteID}/api-keys/{apiKeyID}/models", adminHandler.UpdateSiteAPIKeyModel)
				protected.Post("/sites/{siteID}/api-keys/{apiKeyID}/refresh", adminHandler.RefreshSiteAPIKey)
				protected.Get("/sites/{siteID}/accounts", adminHandler.ListAccounts)
				protected.Get("/sites/{siteID}/accounts/regions", adminHandler.ListAccountRegions)
				protected.Post("/sites/{siteID}/accounts/import", adminHandler.ImportAccounts)
				protected.Post("/sites/{siteID}/accounts/login/start", adminHandler.StartAccountLogin)
				protected.Post("/sites/{siteID}/accounts/login/poll", adminHandler.PollAccountLogin)
				protected.Post("/sites/{siteID}/accounts/refresh", adminHandler.RefreshAccounts)
				protected.Put("/sites/{siteID}/accounts/{credentialID}", adminHandler.UpdateAccount)
				protected.Put("/sites/{siteID}/accounts/{credentialID}/models", adminHandler.UpdateAccountModel)
				protected.Delete("/sites/{siteID}/accounts/{credentialID}", adminHandler.DeleteAccount)
				protected.Post("/sites/{siteID}/accounts/{credentialID}/refresh", adminHandler.RefreshAccount)
				protected.Get("/sites/{siteID}/models", adminHandler.ListSiteModels)
				protected.Get("/site-models", adminHandler.ListAllSiteModels)
				protected.Post("/sites/{siteID}/models", adminHandler.CreateSiteModel)
				protected.Get("/sites/{siteID}/pricing", adminHandler.ListSitePricing)
				protected.Put("/sites/{siteID}/models/status", adminHandler.UpdateSiteModelsStatus)
				protected.Put("/sites/{siteID}/models/{modelID}", adminHandler.UpdateSiteModel)
				protected.Delete("/sites/{siteID}/models/{modelID}", adminHandler.DeleteSiteModel)
				protected.Post("/sites/{siteID}/models/{modelID}/test", adminHandler.TestSiteModel)
				protected.Post("/sites/{siteID}/sync-models", adminHandler.SyncSiteModels)
				protected.Post("/sites/{siteID}/newapi/user-summary", adminHandler.SiteNewAPIUserSummary)
				protected.Post("/sites/{siteID}/newapi/api-key-summary", adminHandler.SiteNewAPIAPIKeySummary)
				protected.Post("/sites/{siteID}/newapi/checkin", adminHandler.SiteNewAPICheckin)
				protected.Post("/oauth/providers/codex/authorize", adminHandler.StartCodexOAuth)
				protected.Post("/oauth/providers/antigravity/authorize", adminHandler.StartAntigravityOAuth)
				protected.Post("/oauth/providers/claude_code/authorize", adminHandler.StartClaudeCodeOAuth)
				protected.Post("/oauth/providers/claude_code/complete", adminHandler.CompleteClaudeCodeOAuth)
				protected.Post("/oauth/providers/{provider}/callback-url", adminHandler.CompleteOAuthCallbackURL)
				protected.Post("/oauth/import", adminHandler.ImportOAuthAccounts)
				protected.Get("/oauth/connections", adminHandler.ListOAuthConnections)
				protected.Get("/oauth/connections/{connectionID}", adminHandler.GetOAuthConnection)
				protected.Post("/oauth/connections/{connectionID}/refresh", adminHandler.RefreshOAuthConnection)
				protected.Get("/oauth/connections/{connectionID}/reset-credits", adminHandler.ListOAuthConnectionResetCredits)
				protected.Post("/oauth/connections/{connectionID}/reset-credit/consume", adminHandler.ConsumeOAuthConnectionResetCredit)
				protected.Post("/oauth/connections/{connectionID}/export", adminHandler.ExportOAuthConnection)
				protected.Put("/oauth/connections/{connectionID}/models", adminHandler.UpdateOAuthConnectionModel)
				protected.Put("/oauth/connections/{connectionID}/models/status", adminHandler.UpdateOAuthConnectionModelsStatus)
				protected.Post("/newapi/user-summary", adminHandler.NewAPIUserSummary)
				protected.Post("/newapi/api-key-summary", adminHandler.NewAPIAPIKeySummary)
				protected.Post("/newapi/checkin", adminHandler.NewAPICheckin)
				protected.Get("/models", adminHandler.ListModels)
				protected.Post("/models", adminHandler.CreateModel)
				protected.Put("/models/{modelID}", adminHandler.UpdateModel)
				protected.Delete("/models/{modelID}", adminHandler.DeleteModel)
				protected.Get("/models/{modelID}/matrix", adminHandler.GetModelMatrix)
				protected.Put("/models/{modelID}/pricing", adminHandler.UpdateModelPricing)
				protected.Post("/models/{modelID}/pricing/reset", adminHandler.ResetModelPricing)
				protected.Post("/models/{modelID}/aliases", adminHandler.CreateModelAlias)
				protected.Delete("/models/{modelID}/aliases/{aliasID}", adminHandler.DeleteModelAlias)
				protected.Get("/model-prices", adminHandler.ListModelPrices)
				protected.Put("/model-prices/bulk", adminHandler.BulkUpdateModelPrices)
				protected.Put("/site-models/{siteModelID}/canonical", adminHandler.BindSiteModelCanonical)
				protected.Put("/site-models/{siteModelID}/pricing", adminHandler.UpdateSiteModelPricing)
				protected.Post("/site-models/{siteModelID}/pricing/reset", adminHandler.ResetSiteModelPricing)
				protected.Post("/sites/{siteID}/pricing/reset-all", adminHandler.ResetSitePricing)
				protected.Get("/site-pricings", adminHandler.ListAllSitePricings)
				protected.Get("/health/sites", adminHandler.ListSiteHealth)
				protected.Get("/routes", adminHandler.ListRoutes)
				protected.Get("/routes/traces", adminHandler.ListRouteTraces)
				protected.Post("/routes/select", adminHandler.SelectRoute)
				protected.Post("/routes/failover", adminHandler.FailoverRoute)
				protected.Get("/routes/cooldowns", adminHandler.ListRouteCooldowns)
				protected.Post("/routes/cooldowns", adminHandler.CreateRouteCooldown)
				protected.Post("/routes/cooldowns/clear", adminHandler.ClearRouteCooldown)
				protected.Get("/routes/candidates", adminHandler.ListRouteCandidates)
				protected.Get("/settings/system-proxy", settingsHandler.GetSystemProxy)
				protected.Put("/settings/system-proxy", settingsHandler.UpdateSystemProxy)
				protected.Post("/settings/system-proxy/test", settingsHandler.TestSystemProxy)
				protected.Get("/settings/general", settingsHandler.GetGeneral)
				protected.Put("/settings/general", settingsHandler.UpdateGeneral)
				protected.Get("/settings/appearance", settingsHandler.GetAppearance)
				protected.With(httpx.LimitRequestBody(cfg.MaxRequestBodyBytes)).Put("/settings/appearance", settingsHandler.UpdateAppearance)
				protected.Get("/appearance/backgrounds/{name}", settingsHandler.ServeAppearanceBackground)
				protected.Get("/settings/portal", settingsHandler.GetPortal)
				protected.Put("/settings/portal", settingsHandler.UpdatePortal)
				protected.Get("/settings/rate-limits", settingsHandler.GetRateLimits)
				protected.Put("/settings/rate-limits", settingsHandler.UpdateRateLimits)
				protected.Post("/settings/backup/export", settingsHandler.ExportBackup)
				protected.Post("/settings/backup/import", settingsHandler.ImportBackup)
				protected.Post("/settings/backup/inspect", settingsHandler.InspectBackup)
				protected.Get("/settings/backup/sections", settingsHandler.GetBackupSections)
				protected.Get("/settings/backup/automatic", settingsHandler.GetAutomaticBackup)
				protected.Put("/settings/backup/automatic", settingsHandler.UpdateAutomaticBackup)
				protected.Post("/settings/backup/automatic/test", settingsHandler.TestAutomaticBackup)
				protected.Get("/settings/backup/automatic/files", settingsHandler.ListAutomaticBackupFiles)
				protected.Post("/settings/backup/automatic/run", settingsHandler.RunAutomaticBackup)
				protected.Post("/settings/backup/automatic/files/inspect", settingsHandler.InspectAutomaticBackupFile)
				protected.Post("/settings/backup/automatic/files/restore", settingsHandler.RestoreAutomaticBackupFile)
				protected.Get("/settings/backup/automatic/files/restore/active", settingsHandler.GetActiveAutomaticBackupRestoreTask)
				protected.Get("/settings/backup/automatic/files/restore/{taskID}", settingsHandler.GetAutomaticBackupRestoreTask)
				protected.Delete("/settings/backup/automatic/files/restore/{taskID}", settingsHandler.CancelAutomaticBackupRestoreTask)
				protected.Delete("/settings/backup/automatic/files", settingsHandler.DeleteAutomaticBackupFile)
			})
		})
	})

	r.Route("/v1", func(v1 chi.Router) {
		v1.Group(func(identity chi.Router) {
			identity.Use(requireAPIKeyIdentity(authService))
			identity.Get("/user/balance", gatewayHandler.UserBalance)
			identity.Get("/portal/overview", gatewayHandler.PortalOverview)
			identity.Get("/portal/summary", gatewayHandler.PortalSummary)
			identity.Get("/portal/requests", gatewayHandler.PortalRequests)
			identity.Get("/portal/models", gatewayHandler.PortalModels)
		})
		v1.Get("/portal/settings", gatewayHandler.PortalSettings)
		v1.Group(func(protected chi.Router) {
			protected.Use(requireAPIKey(authService))
			// Cap JSON text endpoints; image endpoints keep their own upload limit.
			limitBody := httpx.LimitRequestBody(cfg.MaxRequestBodyBytes)
			textLimitBody := httpx.LimitRequestBody(min(cfg.MaxRequestBodyBytes, 32*1024*1024))
			protected.With(limitBody).Post("/chat/completions", gatewayHandler.ChatCompletions)
			protected.With(textLimitBody).Post("/embeddings", gatewayHandler.Embeddings)
			protected.With(textLimitBody).Post("/audio/speech", gatewayHandler.AudioSpeech)
			protected.With(textLimitBody).Post("/systemone", gatewayHandler.TypeSafeSystemOne)
			protected.With(textLimitBody).Post("/plugins/{slug}", gatewayHandler.PluginProtocol)
			protected.Post("/images/generations", gatewayHandler.ImagesGenerations)
			protected.Post("/images/edits", gatewayHandler.ImagesEdits)
			protected.With(limitBody).Post("/messages", gatewayHandler.Messages)
			protected.With(httpx.DecompressRequestBody(cfg.MaxRequestBodyBytes, logger), limitBody).Post("/responses", gatewayHandler.Responses)
			protected.Get("/responses", gatewayHandler.ResponsesWebSocket(cfg.MaxRequestBodyBytes))
			protected.Get("/models", gatewayHandler.Models)
		})
	})

	r.Route("/v1beta", func(v1beta chi.Router) {
		v1beta.Group(func(protected chi.Router) {
			protected.Use(requireAPIKey(authService))
			limitBody := httpx.LimitRequestBody(cfg.MaxRequestBodyBytes)
			protected.Get("/models", gatewayHandler.GeminiModels)
			protected.With(limitBody).Post("/models/{model}:generateContent", gatewayHandler.GeminiGenerateContent(false))
			protected.With(limitBody).Post("/models/{model}:streamGenerateContent", gatewayHandler.GeminiGenerateContent(true))
		})
	})

	r.Route("/internal/agent-llm", func(internal chi.Router) {
		limitBody := httpx.LimitRequestBody(cfg.MaxRequestBodyBytes)
		internal.With(limitBody).Post("/credential", agentLLMHandler.Credential)
		internal.With(limitBody).Post("/credential/renew", agentLLMHandler.Renew)
		internal.With(limitBody).Post("/images", agentLLMHandler.Images)
		// 文件摘要的请求体含最多 300KB 文件内容(JSON 转义后会膨胀),用独立的 2 MiB 上限,
		// 不随全局 MaxRequestBodyBytes 变化,也不影响其它路由。
		internal.With(httpx.LimitRequestBody(agentllm.FastTaskMaxRequestBytes)).Post("/fast-task", agentLLMHandler.FastTask)
		internal.With(limitBody).Post("/v1/responses", agentLLMHandler.Responses)
		internal.With(limitBody).Post("/v1/messages", agentLLMHandler.Messages)
		internal.With(limitBody).Post("/v1/chat/completions", agentLLMHandler.ChatCompletions)
		internal.Get("/v1/models", agentLLMHandler.Models)
	})

	return r, &gatewayHandler, pluginMarket
}

// agentProtocolResolver 返回「模型 → 上游协议」的解析闭包，runner 转发与内置
// Pi 引擎共用。解析失败或候选为空时降级为 openai-responses：错误应发生在实际
// LLM 调用时，而不是会话创建时。
func agentProtocolResolver(logger *slog.Logger, confFile *config.ConfigFile, routerService *routeengine.Service) func(ctx context.Context, model string) (string, error) {
	return func(ctx context.Context, model string) (string, error) {
		if routerService == nil {
			return "openai-responses", nil
		}
		policy, modelPolicy, siteIDs, siteModelIDs := agentproxy.AccessPolicyFromConfig(confFile)
		query := routeengine.CandidateQuery{ModelKey: model}
		if policy == "allow_list" {
			query.AllowedSiteIDs = parseAgentUUIDs(siteIDs)
		}
		if modelPolicy == "allow_list" {
			query.AllowedSiteModelIDs = parseAgentUUIDs(siteModelIDs)
		}
		candidates, err := routerService.Candidates(ctx, query)
		if err != nil {
			// 解析失败（如模型未归一到 canonical、路由服务异常）不能阻断会话创建：
			// 降级为 openai-responses，由网关在实际调用时做协议转换或报出真实路由错误
			logger.Warn("agent protocol resolution failed, falling back to openai-responses", "model", model, "error", err)
			return "openai-responses", nil
		}
		protocol := ""
		for _, candidate := range candidates.Items {
			for _, endpointType := range candidate.Model.SupportedEndpointTypes {
				switch strings.ToLower(strings.TrimSpace(endpointType)) {
				case "anthropic-messages", "messages":
					return "anthropic-messages", nil
				case "openai-response", "openai-responses", "responses":
					protocol = "openai-responses"
				case "openai", "chat", "chat-completions", "openai-chat-completions":
					if protocol == "" {
						protocol = "openai-chat-completions"
					}
				}
			}
		}
		if protocol == "" {
			// 候选为空（冷却/无可用凭证/白名单过滤）同样降级
			logger.Warn("no supported agent protocol from candidates, falling back to openai-responses", "model", model, "candidates", len(candidates.Items))
			return "openai-responses", nil
		}
		return protocol, nil
	}
}

// registerAgentRoutes 注册 /agent 路由组。settings 与 backgrounds 由
// agentproxy 提供;会话/事件/health 与 skills/workspace/config 能力接口走
// 内置 Pi handler。
func registerAgentRoutes(agentRouter chi.Router, cfg config.Config, confFile *config.ConfigFile, agentProxyHandler *agentproxy.Handler, builtin *builtinAgent) {
	agentRouter.Get("/settings", func(w http.ResponseWriter, r *http.Request) {
		agentProxyHandler.GetSettings(w, r, confFile)
	})
	agentRouter.With(httpx.LimitRequestBody(cfg.MaxRequestBodyBytes)).Put("/settings", func(w http.ResponseWriter, r *http.Request) {
		agentProxyHandler.UpdateSettings(w, r, confFile)
	})
	agentRouter.Get("/backgrounds/{name}", agentProxyHandler.ServeBackground)
	h := builtin.handler
	agentRouter.Get("/health", h.Health)
	agentRouter.Get("/skills", h.ListSkills)
	agentRouter.Get("/skills/{name}", h.GetSkill)
	agentRouter.Get("/skills/{name}/file", h.GetSkillFile)
	agentRouter.Put("/skills/{name}", h.PutSkill)
	agentRouter.Delete("/skills/{name}", h.DeleteSkill)
	agentRouter.Get("/workspace/file", h.GetWorkspaceFile)
	agentRouter.Put("/workspace/file", h.PutWorkspaceFile)
	agentRouter.Delete("/workspace/file", h.DeleteWorkspaceFile)
	agentRouter.Get("/config", h.GetConfig)
	agentRouter.Put("/config", h.PutConfig)
	agentRouter.Get("/model-memory", h.ModelMemory)
	agentRouter.Get("/models", h.Models)
	agentRouter.Get("/sessions", h.ListSessions)
	agentRouter.Post("/sessions", h.CreateSession)
	agentRouter.Patch("/sessions/{id}", h.RenameSession)
	agentRouter.Get("/sessions/{id}/transcript", h.SessionTranscript)
	agentRouter.Get("/sessions/{id}/stats", h.SessionStats)
	agentRouter.Delete("/sessions/{id}", h.DeleteSession)
	agentRouter.Post("/sessions/{id}/fork", h.ForkSession)
	agentRouter.Get("/sessions/{id}/files", h.SessionFile)
	agentRouter.Head("/sessions/{id}/files", h.SessionFile)
	agentRouter.Get("/sessions/{id}/changes", h.SessionChanges)
	agentRouter.Get("/sessions/{id}/changes/{turnID}/diff", h.SessionChangeDiff)
	agentRouter.Get("/sessions/{id}/artifacts", h.SessionArtifacts)
	agentRouter.Post("/sessions/{id}/retry", h.RetrySession)
	agentRouter.Post("/sessions/{id}/stop", h.StopSession)
	agentRouter.Post("/sessions/{id}/suggestion", h.Suggestion)
	agentRouter.Post("/sessions/{id}/grant-access", h.GrantAccess)
	agentRouter.Post("/sessions/{id}/answer-question", h.AnswerQuestion)
	agentRouter.Post("/sessions/{id}/revoke-access", h.RevokeAccess)
	agentRouter.Get("/sessions/{id}/events", h.SessionEvents)
}

// registerAdminProfileRoutes 注册会自我扩权的账号安全接口: 设备令牌与管理员
// 同权, 但这些接口只允许 Web 会话 / Access Token 调用, 因此统一加
// DenyDeviceActor。
func registerAdminProfileRoutes(protected chi.Router, adminHandler admin.Handler) {
	webOnly := protected.With(auth.DenyDeviceActor)
	webOnly.Put("/profile/account", adminHandler.UpdateProfileAccount)
	webOnly.Put("/profile/password", adminHandler.UpdateProfilePassword)
	webOnly.Get("/profile/access-token", adminHandler.GetProfileAccessToken)
	webOnly.Post("/profile/access-token", adminHandler.CreateProfileAccessToken)
	webOnly.Put("/profile/access-token/enabled", adminHandler.UpdateProfileAccessTokenEnabled)
	webOnly.Delete("/profile/access-token", adminHandler.DeleteProfileAccessToken)
	webOnly.Post("/profile/totp/setup", adminHandler.SetupProfileTOTP)
	webOnly.Post("/profile/totp/enable", adminHandler.EnableProfileTOTP)
	webOnly.Delete("/profile/totp", adminHandler.DisableProfileTOTP)
}

// registerAdminDeviceRoutes 注册设备管理接口: 创建配对码、列出与吊销设备
// 同样只允许 Web 会话 / Access Token; /devices/self 反而只给设备令牌用。
func registerAdminDeviceRoutes(protected chi.Router, adminHandler admin.Handler) {
	webOnly := protected.With(auth.DenyDeviceActor)
	webOnly.Post("/devices/pairings", adminHandler.CreateDevicePairing)
	webOnly.Get("/devices", adminHandler.ListDevices)
	webOnly.Delete("/devices/{id}", adminHandler.RevokeDevice)
	webOnly.Post("/devices/{id}/push/test", adminHandler.TestDevicePush)
	webOnly.Post("/devices/revoke-all", adminHandler.RevokeAllDevices)
	protected.Get("/devices/self", adminHandler.GetDeviceSelf)
	protected.Delete("/devices/self", adminHandler.DeleteDeviceSelf)
	protected.Put("/devices/self/push", adminHandler.PutDevicePush)
	protected.Delete("/devices/self/push", adminHandler.DeleteDevicePush)
	protected.Get("/devices/self/push", adminHandler.GetDevicePush)
	protected.Put("/devices/self/push/prefs", adminHandler.PutDevicePushPrefs)
}

func requireAdmin(authService *auth.Service) func(http.Handler) http.Handler {
	if authService == nil {
		return authUnavailable
	}

	return authService.RequireAdminSession
}

func requireAdminCSRF(authService *auth.Service) func(http.Handler) http.Handler {
	if authService == nil {
		return authUnavailable
	}

	return authService.RequireAdminCSRF
}

func requireAPIKey(authService *auth.Service) func(http.Handler) http.Handler {
	if authService == nil {
		return authUnavailable
	}

	return authService.RequireAPIKey
}

func requireAPIKeyIdentity(authService *auth.Service) func(http.Handler) http.Handler {
	if authService == nil {
		return authUnavailable
	}

	return authService.RequireAPIKeyIdentity
}

func authUnavailable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusServiceUnavailable, "auth_unavailable", "auth service is not available")
	})
}

const (
	backupImportPath             = "/api/v1/settings/backup/import"
	backupExportPath             = "/api/v1/settings/backup/export"
	automaticBackupRunPath       = "/api/v1/settings/backup/automatic/run"
	automaticBackupRestorePath   = "/api/v1/settings/backup/automatic/files/restore"
	downloadPathPrefix           = "/api/v1/downloads/"
	backupTransferRequestTimeout = 10 * time.Minute
	// agent 会话命令的同步执行上限：/compact 压缩大上下文（一次摘要 LLM 调用 + 瞬态重试）
	agentLongOpRequestTimeout = 10 * time.Minute
)

func routeAwareTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	base := middleware.Timeout(timeout)
	backupTransfer := middleware.Timeout(backupTransferRequestTimeout)
	// agent 会话命令可能是同步长操作（/compact 压缩大上下文是一次完整 LLM 调用），
	// 30s 默认超时会把它们拦腰掐断
	agentLongOp := middleware.Timeout(agentLongOpRequestTimeout)
	return func(next http.Handler) http.Handler {
		timeoutHandler := base(next)
		backupTransferTimeoutHandler := backupTransfer(next)
		agentLongOpTimeoutHandler := agentLongOp(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/v1/responses" && strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			if isBackupTransferRequest(r) {
				backupTransferTimeoutHandler.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/agent/sessions") {
				agentLongOpTimeoutHandler.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/api/v1/dashboard/resources/stream" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/api/v1/traffic-flow/stream" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/agent/sessions/") && strings.HasSuffix(r.URL.Path, "/events") {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodPost && (r.URL.Path == "/internal/agent-llm/images" || r.URL.Path == "/internal/agent-llm/fast-task" || strings.HasPrefix(r.URL.Path, "/internal/agent-llm/v1/")) {
				next.ServeHTTP(w, r)
				return
			}
			gatewayPath := r.URL.Path
			geminiGatewayPath := strings.HasPrefix(gatewayPath, "/v1beta/models/") && (strings.HasSuffix(gatewayPath, ":generateContent") || strings.HasSuffix(gatewayPath, ":streamGenerateContent"))
			if r.Method == http.MethodPost && (gatewayPath == "/v1/chat/completions" || gatewayPath == "/v1/responses" || gatewayPath == "/v1/images/generations" || gatewayPath == "/v1/images/edits" || gatewayPath == "/v1/messages" || gatewayPath == "/v1/audio/speech" || geminiGatewayPath) {
				next.ServeHTTP(w, r)
				return
			}
			timeoutHandler.ServeHTTP(w, r)
		})
	}
}

func isBackupTransferRequest(r *http.Request) bool {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, downloadPathPrefix) {
		return true
	}
	return r.Method == http.MethodPost && (r.URL.Path == backupImportPath || r.URL.Path == backupExportPath || r.URL.Path == automaticBackupRunPath || r.URL.Path == automaticBackupRestorePath)
}

func spaHandler(staticDir string) http.Handler {
	return newSPAHandler(staticDir, nil)
}

func newSPAHandler(staticDir string, authService *auth.Service) http.Handler {
	fs := newCompressedFileServer(staticDir, http.FileServer(http.Dir(staticDir)))
	index := filepath.Join(staticDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(staticDir, filepath.Clean("/"+r.URL.Path))
		info, err := os.Stat(path)
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || os.IsNotExist(err) {
			// SPA 回退到 index.html：不进缓存，刷新必须拿到这次发版的页面
			serveAppIndex(w, r, index, authService)
			return
		}
		switch {
		case err == nil && info.IsDir():
			// 其余目录请求由 FileServer 处理，同样不进缓存
			w.Header().Set("Cache-Control", "no-store")
		case mustRevalidateStaticFile(filepath.Base(path)):
			// Service Worker 与构建号不能被浏览器或中间缓存留住，否则刷新后仍是旧脚本
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			// Vite 构建产物文件名带内容哈希，可安全长缓存
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			// public 目录原样拷贝的文件（agent-backdrop.png、favicon 等）名字不带哈希，
			// 给中等缓存平衡更新时效与重复加载
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		fs.ServeHTTP(w, r)
	})
}

func serveAppIndex(w http.ResponseWriter, r *http.Request, indexPath string, authService *auth.Service) {
	w.Header().Set("Cache-Control", "no-store")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if authService != nil {
		if initialized, ok := authService.CachedBootstrapInitialized(); ok {
			httpx.SetBootstrapInitializedCookie(w, r, initialized)
			body = injectBootstrapState(body, initialized)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func injectBootstrapState(body []byte, initialized bool) []byte {
	value := "false"
	if initialized {
		value = "true"
	}
	snippet := []byte("<script>window.__XLYRA_BOOTSTRAP__={initialized:" + value + "}</script>")
	lower := bytes.ToLower(body)
	if i := bytes.Index(lower, []byte("</head>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		out = append(out, body[i:]...)
		return out
	}
	return append(snippet, body...)
}

func mustRevalidateStaticFile(name string) bool {
	switch name {
	case "index.html", "sw.js", "registerSW.js", "version.json":
		return true
	}
	if strings.HasSuffix(name, ".webmanifest") {
		return true
	}
	return strings.HasPrefix(name, "workbox-") && strings.HasSuffix(name, ".js")
}
