package agentctl

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/catalog"
	"xlyra/server/internal/config"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
	"xlyra/server/internal/store"
	"xlyra/server/internal/usage"
)

func TestCatalogAndRouteCapabilitiesRegistered(t *testing.T) {
	expected := []struct {
		resource string
		action   string
		tier     int
	}{
		{"model", "list", Tier1ReadOnly},
		{"model", "get", Tier1ReadOnly},
		{"model", "matrix", Tier1ReadOnly},
		{"model", "create", Tier2Managed},
		{"model", "update", Tier2Managed},
		{"model", "delete", Tier3HighRisk},
		{"model", "merge", Tier3HighRisk},
		{"model", "alias-add", Tier2Managed},
		{"model", "alias-delete", Tier2Managed},
		{"model", "pricing-set", Tier2Managed},
		{"model", "pricing-reset", Tier2Managed},
		{"site-model", "list", Tier1ReadOnly},
		{"site-model", "enable", Tier2Managed},
		{"site-model", "disable", Tier2Managed},
		{"site-model", "update", Tier2Managed},
		{"site-model", "batch-status", Tier2Managed},
		{"site-model", "bind-canonical", Tier2Managed},
		{"route", "overview", Tier1ReadOnly},
		{"route", "list", Tier1ReadOnly},
		{"route", "candidates", Tier1ReadOnly},
		{"route", "select", Tier1ReadOnly},
		{"route", "failover", Tier1ReadOnly},
		{"route", "traces", Tier1ReadOnly},
		{"route", "get", Tier1ReadOnly},
		{"route", "test", Tier1ReadOnly},
		{"cooldown", "list", Tier1ReadOnly},
		{"cooldown", "get", Tier1ReadOnly},
		{"cooldown", "create", Tier2Managed},
		{"cooldown", "activate", Tier2Managed},
		{"cooldown", "clear", Tier2Managed},
	}

	for _, tc := range expected {
		spec, ok := LookupOpSpec(tc.resource, tc.action)
		if !ok {
			t.Errorf("operation %s/%s is not registered in capability catalog", tc.resource, tc.action)
			continue
		}
		if spec.Tier != tc.tier {
			t.Errorf("operation %s/%s tier = %d, want %d", tc.resource, tc.action, spec.Tier, tc.tier)
		}
	}
}

func TestUDSResponseVersionHeadersAndUnknownOpHint(t *testing.T) {
	cf, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	eng := NewEngine(Deps{
		Version:  "test-2.0.0",
		ConfFile: cf,
	})

	sockDir, err := os.MkdirTemp("/tmp", "xctl-hdr-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "ctl.sock")

	srv, err := StartUDSServer(eng, UDSConfig{
		SocketPath: sockPath,
		Caller: Caller{
			Channel:        ChannelAgentCLI,
			RunID:          "run-hdr-1",
			PermissionMode: "full",
			Policy:         DefaultBuiltinAgentPolicy(),
			Quota:          NewRunQuota(20),
		},
	})
	if err != nil {
		t.Fatalf("StartUDSServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}

	post := func(req CallRequest) (*http.Response, Envelope) {
		body, _ := json.Marshal(req)
		httpReq, _ := http.NewRequest(http.MethodPost, "http://ctl.local/v1/call", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set(HeaderProtocol, strconv.Itoa(ProtocolVersion))
		httpReq.Header.Set(HeaderNonce, srv.Nonce())
		resp, err := client.Do(httpReq)
		if err != nil {
			t.Fatalf("http post %s/%s: %v", req.Resource, req.Action, err)
		}
		defer resp.Body.Close()
		var env Envelope
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return resp, env
	}

	resp, env := post(CallRequest{Protocol: ProtocolVersion, Resource: "status", Action: "get"})
	if resp.StatusCode != http.StatusOK || !env.OK {
		t.Fatalf("status/get failed: status=%d env=%+v", resp.StatusCode, env)
	}
	if got := resp.Header.Get(HeaderSchemaVersion); got != strconv.Itoa(SchemaVersion) {
		t.Fatalf("%s = %q, want %d", HeaderSchemaVersion, got, SchemaVersion)
	}
	if got := resp.Header.Get(HeaderServerVersion); got != "test-2.0.0" {
		t.Fatalf("%s = %q, want test-2.0.0", HeaderServerVersion, got)
	}
	data, _ := env.Data.(map[string]any)
	if got, _ := data["schema_version"].(float64); int(got) != SchemaVersion {
		t.Fatalf("status schema_version = %v, want %d", data["schema_version"], SchemaVersion)
	}

	resp2, errEnv := post(CallRequest{Protocol: ProtocolVersion, Resource: "nonexistent", Action: "boom"})
	if resp2.StatusCode != http.StatusNotFound || errEnv.OK || errEnv.Error == nil {
		t.Fatalf("expected 404 for unknown op, got status=%d env=%+v", resp2.StatusCode, errEnv)
	}
	if !strings.Contains(errEnv.Error.Hint, "xlyra schema") {
		t.Fatalf("expected helpful hint on unknown op, got %q", errEnv.Error.Hint)
	}
}

func TestGroupMatrixRowsDeduplicatesPricingGroups(t *testing.T) {
	sm1 := uuid.New()
	sm2 := uuid.New()
	site1 := uuid.New()
	site2 := uuid.New()

	rows := []store.CanonicalModelMatrixRow{
		{
			SiteID:            site1,
			SiteName:          "Site One",
			SiteSlug:          "site-one",
			SiteType:          "openai",
			SiteEnabled:       true,
			SiteModelID:       sm1,
			UpstreamModelName: "deepseek-v4.1-flash",
			ModelStatus:       "active",
			GroupName:         sql.NullString{String: "default", Valid: true},
			BillingType:       sql.NullString{String: "token", Valid: true},
			InputValue:        sql.NullFloat64{Float64: 0.1, Valid: true},
			OutputValue:       sql.NullFloat64{Float64: 0.2, Valid: true},
			Currency:          sql.NullString{String: "USD", Valid: true},
		},
		{
			SiteID:            site1,
			SiteName:          "Site One",
			SiteSlug:          "site-one",
			SiteType:          "openai",
			SiteEnabled:       true,
			SiteModelID:       sm1,
			UpstreamModelName: "deepseek-v4.1-flash",
			ModelStatus:       "active",
			GroupName:         sql.NullString{String: "vip", Valid: true},
			BillingType:       sql.NullString{String: "token", Valid: true},
			InputValue:        sql.NullFloat64{Float64: 0.08, Valid: true},
			OutputValue:       sql.NullFloat64{Float64: 0.16, Valid: true},
			Currency:          sql.NullString{String: "USD", Valid: true},
		},
		{
			SiteID:            site2,
			SiteName:          "Site Two",
			SiteSlug:          "site-two",
			SiteType:          "new_api",
			SiteEnabled:       false,
			SiteModelID:       sm2,
			UpstreamModelName: "deepseek-flash",
			ModelStatus:       "active",
		},
	}

	grouped := ctlGroupedMatrixItems(rows)
	if len(grouped) != 2 {
		t.Fatalf("len(grouped) = %d, want 2", len(grouped))
	}
	if grouped[0]["site_model_id"] != sm1.String() {
		t.Fatalf("first entry site_model_id = %v, want %s", grouped[0]["site_model_id"], sm1)
	}
	groups := grouped[0]["pricing"].([]map[string]any)
	if len(groups) != 2 {
		t.Fatalf("pricing len = %d, want 2", len(groups))
	}
	if grouped[1]["site_model_id"] != sm2.String() {
		t.Fatalf("second entry site_model_id = %v, want %s", grouped[1]["site_model_id"], sm2)
	}
}

func TestDevPostgresCatalogRouteCLIEndToEnd(t *testing.T) {
	cliBin, err := filepath.Abs(filepath.Join("..", "..", "..", "data", "bin", "xlyra"))
	if err != nil {
		t.Fatalf("resolve cli bin: %v", err)
	}
	if info, statErr := os.Stat(cliBin); statErr != nil || info.IsDir() {
		t.Skip("xlyra binary not built at data/bin/xlyra; skipping real binary E2E test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := config.Config{
		AppEnv:           "test",
		DBConnectTimeout: 2 * time.Second,
		DBMinConns:       0,
		DBMaxConns:       2,
		PostgresDSN:      strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		DBHost:           envOrDefault("DB_HOST", "127.0.0.1"),
		DBPort:           5432,
		DBName:           envOrDefault("DB_NAME", "xlyra"),
		DBUser:           envOrDefault("DB_USER", "postgres"),
		DBPassword:       envOrDefault("DB_PASSWORD", "postgres"),
		DBSSLMode:        envOrDefault("DB_SSLMODE", "disable"),
	}
	if rawPort := strings.TrimSpace(os.Getenv("DB_PORT")); rawPort != "" {
		if p, parseErr := strconv.Atoi(rawPort); parseErr == nil {
			cfg.DBPort = p
		}
	}
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Skipf("dev PostgreSQL unavailable: %v", err)
	}
	defer db.Close()

	cf, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}

	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	siteRepo := store.NewSiteRepository(db.DB())
	smRepo := store.NewSiteModelRepository(db.DB())
	credRepo := store.NewSiteCredentialRepository(db.DB())

	catalogSvc := catalog.NewService(db, cf)
	siteSvc := site.NewService(db, "0123456789abcdef0123456789abcdef")
	routerSvc := routeengine.NewService(db)
	usageSvc := usage.NewService(db)

	var createdSiteIDs []uuid.UUID
	var createdModelIDs []uuid.UUID
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		gormDB := db.DB().WithContext(cleanCtx)
		for _, sid := range createdSiteIDs {
			_ = gormDB.Exec("DELETE FROM route_cooldowns WHERE site_id = ?", sid).Error
			_ = gormDB.Exec("DELETE FROM site_model_pricing WHERE site_model_id IN (SELECT id FROM site_models WHERE site_id = ?)", sid).Error
			_ = gormDB.Exec("DELETE FROM site_models WHERE site_id = ?", sid).Error
			_ = gormDB.Exec("DELETE FROM site_credentials WHERE site_id = ?", sid).Error
			_ = gormDB.Exec("DELETE FROM sites WHERE id = ?", sid).Error
		}
		for _, mid := range createdModelIDs {
			_ = gormDB.Exec("DELETE FROM canonical_model_aliases WHERE canonical_model_id = ?", mid).Error
			_ = gormDB.Exec("DELETE FROM canonical_models WHERE id = ?", mid).Error
		}
	})

	// Seed 2 enabled sites with API key credentials.
	site1, err := siteRepo.Create(ctx, store.CreateSiteParams{
		Name:            "CLI E2E Site 1 " + tag,
		Slug:            "cli-e2e-s1-" + tag,
		SiteType:        "openai",
		BaseURL:         "https://s1-" + tag + ".example.test",
		Status:          "active",
		Enabled:         true,
		RoutingPriority: 1.0,
	})
	if err != nil {
		t.Fatalf("create site1: %v", err)
	}
	createdSiteIDs = append(createdSiteIDs, site1.ID)

	site2, err := siteRepo.Create(ctx, store.CreateSiteParams{
		Name:            "CLI E2E Site 2 " + tag,
		Slug:            "cli-e2e-s2-" + tag,
		SiteType:        "openai",
		BaseURL:         "https://s2-" + tag + ".example.test",
		Status:          "active",
		Enabled:         true,
		RoutingPriority: 1.0,
	})
	if err != nil {
		t.Fatalf("create site2: %v", err)
	}
	createdSiteIDs = append(createdSiteIDs, site2.ID)

	for _, sid := range []uuid.UUID{site1.ID, site2.ID} {
		if _, err := credRepo.Upsert(ctx, store.UpsertSiteCredentialParams{
			SiteID:          sid,
			CredentialType:  "api_key",
			EncryptedSecret: "encrypted-placeholder",
			MaskedSecret:    "sk-***test",
		}); err != nil {
			t.Fatalf("upsert site credential: %v", err)
		}
	}

	targetKey := "e2e-flash-" + tag
	sourceKey := "e2e-v41-flash-" + tag
	targetModel, err := catalogSvc.Create(ctx, catalog.UpsertCanonicalModelInput{
		ModelKey:     targetKey,
		DisplayName:  "E2E Flash " + tag,
		Provider:     "deepseek",
		Category:     "chat",
		Capabilities: map[string]any{"context_window": 131072, "tools": true},
	})
	if err != nil {
		t.Fatalf("create target model: %v", err)
	}
	createdModelIDs = append(createdModelIDs, targetModel.ID)

	sourceModel, err := catalogSvc.Create(ctx, catalog.UpsertCanonicalModelInput{
		ModelKey:    sourceKey,
		DisplayName: "E2E V4.1 Flash " + tag,
		Provider:    "deepseek",
		Category:    "chat",
	})
	if err != nil {
		t.Fatalf("create source model: %v", err)
	}
	createdModelIDs = append(createdModelIDs, sourceModel.ID)

	sm1, err := smRepo.Upsert(ctx, store.UpsertSiteModelParams{
		SiteID:       site1.ID,
		UpstreamName: targetKey,
		DisplayName:  targetKey,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("upsert sm1: %v", err)
	}
	sm2, err := smRepo.Upsert(ctx, store.UpsertSiteModelParams{
		SiteID:       site1.ID,
		UpstreamName: targetKey + "-extra",
		DisplayName:  targetKey + "-extra",
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("upsert sm2: %v", err)
	}
	sm3, err := smRepo.Upsert(ctx, store.UpsertSiteModelParams{
		SiteID:       site2.ID,
		UpstreamName: sourceKey,
		DisplayName:  sourceKey,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("upsert sm3: %v", err)
	}

	if _, err := catalogSvc.BindSiteModel(ctx, sm1.ID, &targetModel.ID); err != nil {
		t.Fatalf("bind sm1: %v", err)
	}
	if _, err := catalogSvc.BindSiteModel(ctx, sm3.ID, &sourceModel.ID); err != nil {
		t.Fatalf("bind sm3: %v", err)
	}
	// sm2 intentionally left unmatched initially so we can test --canonical unmatched + bind-canonical via CLI.

	var catalogChanged atomic.Int32
	eng := NewEngine(Deps{
		Version:          "e2e-2.0.0",
		ConfFile:         cf,
		DB:               db,
		Sites:            siteSvc,
		Catalog:          catalogSvc,
		Router:           routerSvc,
		Usage:            usageSvc,
		OnCatalogChanged: func() { catalogChanged.Add(1) },
	})

	sockDir, err := os.MkdirTemp("/tmp", "xctl-pg-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "ctl.sock")

	srv, err := StartUDSServer(eng, UDSConfig{
		SocketPath: sockPath,
		Caller: Caller{
			Channel:        ChannelAgentCLI,
			RunID:          "run-pg-e2e",
			PermissionMode: "full",
			Policy:         DefaultBuiltinAgentPolicy(),
			Quota:          NewRunQuota(50),
		},
	})
	if err != nil {
		t.Fatalf("StartUDSServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })

	runCLI := func(args ...string) (int, Envelope, string) {
		t.Helper()
		cmd := exec.Command(cliBin, args...)
		cmd.Env = []string{
			"PATH=/usr/bin:/bin",
			"HOME=" + t.TempDir(),
			"LANG=C.UTF-8",
			"XLYRA_CTL_SOCK=" + sockPath,
			"XLYRA_CTL_NONCE=" + srv.Nonce(),
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		code := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				t.Fatalf("exec %v: %v (stderr=%s)", args, runErr, stderr.String())
			}
		}
		var env Envelope
		if raw := strings.TrimSpace(stdout.String()); raw != "" {
			_ = json.Unmarshal([]byte(raw), &env)
		}
		return code, env, stdout.String() + stderr.String()
	}

	// 1. status reports compatible=true and schema_version=2.
	code, env, raw := runCLI("status")
	if code != 0 || !env.OK {
		t.Fatalf("status failed: code=%d raw=%s", code, raw)
	}
	statusData := env.Data.(map[string]any)
	if compatible, _ := statusData["compatible"].(bool); !compatible {
		t.Fatalf("expected compatible=true, got %+v", statusData)
	}

	// 2. site-model list filtering (--site, --canonical unmatched, --fields, pagination).
	code, env, raw = runCLI("site-model", "list", "--site", site1.Slug, "--canonical", "unmatched", "--fields", "id,site_slug,upstream_model_name")
	if code != 0 || !env.OK {
		t.Fatalf("site-model list unmatched failed: code=%d raw=%s", code, raw)
	}
	smList := env.Data.(map[string]any)
	items := smList["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != sm2.ID.String() {
		t.Fatalf("expected only sm2 in unmatched list, got %+v", smList)
	}
	if len(items[0].(map[string]any)) != 3 {
		t.Fatalf("expected --fields projection to keep 3 fields, got %+v", items[0])
	}

	// 3. site-model bind-canonical binds sm2 -> targetKey via CLI.
	code, env, raw = runCLI("site-model", "bind-canonical", sm2.ID.String(), targetKey)
	if code != 0 || !env.OK {
		t.Fatalf("site-model bind-canonical failed: code=%d raw=%s", code, raw)
	}
	if catalogChanged.Load() < 1 {
		t.Fatalf("expected OnCatalogChanged to be called after bind-canonical")
	}

	// 4. site-model list pagination (--page 1 --page-size 1) on site1 + targetKey (2 items).
	code, env, raw = runCLI("site-model", "list", "--site", site1.Slug, "--canonical", targetKey, "--page", "1", "--page-size", "1")
	if code != 0 || !env.OK {
		t.Fatalf("site-model list paginated failed: code=%d raw=%s", code, raw)
	}
	paged := env.Data.(map[string]any)
	if int(paged["total"].(float64)) != 2 || int(paged["count"].(float64)) != 1 || paged["has_more"] != true {
		t.Fatalf("unexpected pagination data: %+v", paged)
	}
	if env.Meta == nil || env.Meta.NextPage == nil {
		t.Fatalf("expected Meta.NextPage to be set, got %+v", env.Meta)
	}

	// 4.5. model update must preserve existing capabilities when capabilities is omitted.
	code, env, raw = runCLI("model", "update", targetKey, "--display-name", "E2E Flash Updated "+tag)
	if code != 0 || !env.OK {
		t.Fatalf("model update failed: code=%d raw=%s", code, raw)
	}
	reloadedTarget, err := store.NewCanonicalModelRepository(db.DB()).GetByID(ctx, targetModel.ID)
	if err != nil {
		t.Fatalf("reload target model: %v", err)
	}
	var reloadedCaps map[string]any
	if err := json.Unmarshal(reloadedTarget.Capabilities, &reloadedCaps); err != nil || reloadedCaps["context_window"] != float64(131072) {
		t.Fatalf("model update wiped capabilities: got %s", string(reloadedTarget.Capabilities))
	}

	// 4.6. site-model bind-canonical with nonexistent UUID + none must return exit 3 (not 200 NoOp).
	code, env, raw = runCLI("site-model", "bind-canonical", uuid.NewString(), "none")
	if code != ExitNotFound || env.OK {
		t.Fatalf("bind-canonical nonexistent UUID expected exit %d, got code=%d raw=%s", ExitNotFound, code, raw)
	}

	// 5. Even if targetKey ALREADY has a manual alias equal to sourceKey (the exact bug-report split scenario!),
	// model merge must resolve sourceKey to the exact shell model (not alias-redirect to targetKey).
	if _, err := catalogSvc.AddAlias(ctx, targetModel.ID, sourceKey); err != nil {
		t.Fatalf("add conflicting manual alias %s on target: %v", sourceKey, err)
	}
	code, env, raw = runCLI("model", "merge", sourceKey, targetKey)
	if code != 0 || !env.OK {
		t.Fatalf("model merge failed: code=%d raw=%s", code, raw)
	}

	// 5.5. Destructive model delete on an alias-only name must reject with 404 (never delete targetKey).
	aliasOnly := "e2e-alias-only-" + tag
	if _, err := catalogSvc.AddAlias(ctx, targetModel.ID, aliasOnly); err != nil {
		t.Fatalf("add aliasOnly: %v", err)
	}
	code, env, raw = runCLI("model", "delete", aliasOnly)
	if code != ExitNotFound || env.OK {
		t.Fatalf("model delete on alias-only name expected exit %d, got code=%d raw=%s", ExitNotFound, code, raw)
	}

	// 6. model get <targetKey> --matrix returns matrix with all 3 site models across 2 sites, and unified counts.
	code, env, raw = runCLI("model", "get", targetKey, "--matrix")
	if code != 0 || !env.OK {
		t.Fatalf("model get --matrix failed: code=%d raw=%s", code, raw)
	}
	modelData := env.Data.(map[string]any)
	matrixSlice, ok := modelData["matrix"].([]any)
	if !ok || len(matrixSlice) != 3 {
		t.Fatalf("expected 3 deduplicated matrix entries, got %+v", modelData["matrix"])
	}
	if int(modelData["active_site_model_count"].(float64)) != 3 || int(modelData["active_site_count"].(float64)) != 2 {
		t.Fatalf("expected active_site_model_count=3 and active_site_count=2, got %+v", modelData)
	}

	// 7. route candidates / select / failover / overview / traces all work via CLI (including via merged alias sourceKey!).
	code, env, raw = runCLI("route", "candidates", sourceKey)
	if code != 0 || !env.OK {
		t.Fatalf("route candidates via alias failed: code=%d raw=%s", code, raw)
	}
	candData := env.Data.(map[string]any)
	if int(candData["routable_candidate_count"].(float64)) != 3 || int(candData["active_site_model_count"].(float64)) != 3 {
		t.Fatalf("route candidates counts mismatch: %+v", candData)
	}

	// 7.5. route candidates --limit 1 must slice items to 1 while preserving true routable counts (3 candidates / 2 sites).
	code, env, raw = runCLI("route", "candidates", targetKey, "--limit", "1")
	if code != 0 || !env.OK {
		t.Fatalf("route candidates --limit 1 failed: code=%d raw=%s", code, raw)
	}
	limitedCand := env.Data.(map[string]any)
	if int(limitedCand["count"].(float64)) != 1 || int(limitedCand["routable_candidate_count"].(float64)) != 3 || int(limitedCand["routable_site_count"].(float64)) != 2 {
		t.Fatalf("route candidates --limit 1 skewed counts: %+v", limitedCand)
	}

	// 7.6. pricing list --site validates site existence and filters by SiteID.
	code, env, raw = runCLI("pricing", "list", "--site", site1.Slug)
	if code != 0 || !env.OK {
		t.Fatalf("pricing list --site failed: code=%d raw=%s", code, raw)
	}
	code, env, raw = runCLI("pricing", "list", "--site", "nonexistent-site-"+tag)
	if code != ExitNotFound || env.OK {
		t.Fatalf("pricing list nonexistent --site expected exit %d, got code=%d raw=%s", ExitNotFound, code, raw)
	}

	code, env, raw = runCLI("route", "select", "--model", sourceKey)
	if code != 0 || !env.OK {
		t.Fatalf("route select failed: code=%d raw=%s", code, raw)
	}

	code, env, raw = runCLI("route", "failover", "--model", targetKey, "--failed-site-ids", site1.Slug)
	if code != 0 || !env.OK {
		t.Fatalf("route failover failed: code=%d raw=%s", code, raw)
	}
	failoverData := env.Data.(map[string]any)
	selectedObj, _ := failoverData["selected"].(map[string]any)
	if selectedObj == nil {
		t.Fatalf("expected failover to select site2, got %+v", failoverData)
	}

	code, env, raw = runCLI("route", "overview", "--window-hours", "1")
	if code != 0 || !env.OK {
		t.Fatalf("route overview failed: code=%d raw=%s", code, raw)
	}
	code, env, raw = runCLI("route", "traces", "--model", targetKey, "--limit", "5")
	if code != 0 || !env.OK {
		t.Fatalf("route traces failed: code=%d raw=%s", code, raw)
	}

	// 8. cooldown create & clear by site slug work cleanly, and nonexistent UUID returns 404.
	code, env, raw = runCLI("cooldown", "create", "--site", uuid.NewString(), "--duration", "120", "--reason", "missing")
	if code != ExitNotFound || env.OK {
		t.Fatalf("cooldown create nonexistent site UUID expected exit %d, got code=%d raw=%s", ExitNotFound, code, raw)
	}
	code, env, raw = runCLI("cooldown", "create", "--site", site1.Slug, "--duration", "120", "--reason", "cli-e2e")
	if code != 0 || !env.OK {
		t.Fatalf("cooldown create failed: code=%d raw=%s", code, raw)
	}
	code, env, raw = runCLI("cooldown", "clear", "--site", site1.Slug)
	if code != 0 || !env.OK {
		t.Fatalf("cooldown clear failed: code=%d raw=%s", code, raw)
	}
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Keep fmt imported if needed for debugging helpers.
var _ = fmt.Sprintf
