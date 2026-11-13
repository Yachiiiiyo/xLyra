package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"xlyra/server/internal/analytics"
	"xlyra/server/internal/config"
)

func TestParseAnalyticsUsageParamsContributionFlag(t *testing.T) {
	t.Parallel()

	handler := Handler{timeZone: config.LoadTimeZone("UTC")}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/usage?include_contributions=false", nil)
	params, ok := handler.parseAnalyticsUsageParams(recorder, request)
	if !ok || params.IncludeContributions == nil || *params.IncludeContributions {
		t.Fatalf("parseAnalyticsUsageParams = %#v, %t", params, ok)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/analytics/usage?include_contributions=invalid", nil)
	if _, ok := handler.parseAnalyticsUsageParams(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid include_contributions status = %d, ok = %t", recorder.Code, ok)
	}
}

func TestAnalyticsUsageAbandonsCancelledRequest(t *testing.T) {
	t.Parallel()

	handler := Handler{analytics: &analytics.Service{}}
	target := "/api/v1/analytics/usage?from=2026-11-06&to=2026-11-12&group_by=none"
	req := adminTestRequest(http.MethodGet, target, "")
	assertAdminErrorCode(t, adminPerform(handler.AnalyticsUsage, req), http.StatusInternalServerError, "analytics_usage_failed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := adminPerform(handler.AnalyticsUsage, req.WithContext(ctx))
	if cancelled.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", cancelled.Code, statusClientClosedRequest)
	}
}
