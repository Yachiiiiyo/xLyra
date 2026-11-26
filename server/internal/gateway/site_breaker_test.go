package gateway

import (
	"gorm.io/gorm"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

type breakerClock struct{ now time.Time }

func (c *breakerClock) Now() time.Time          { return c.now }
func (c *breakerClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestSiteBreakers() (*siteBreakers, *breakerClock) {
	clock := &breakerClock{now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	breakers := newSiteBreakers()
	breakers.now = clock.Now
	return breakers, clock
}

func feed(breakers *siteBreakers, clock *breakerClock, siteID uuid.UUID, ok, fail int) (siteBreakerOpening, bool) {
	for i := 0; i < ok; i++ {
		breakers.recordSuccess(siteID, false)
		clock.Advance(time.Second)
	}
	var opening siteBreakerOpening
	var opened bool
	for i := 0; i < fail; i++ {
		if o, did := breakers.recordFailure(siteID); did {
			opening, opened = o, true
		}
		clock.Advance(time.Second)
	}
	return opening, opened
}

func TestSiteBreakerStaysClosedForNoisyOrColdSites(t *testing.T) {
	t.Parallel()

	breakers, clock := newTestSiteBreakers()
	site := uuid.New()
	// A 15% failure rate (the zeroapi 502 pattern) is noise, not an outage.
	if _, opened := feed(breakers, clock, site, 85, 15); opened {
		t.Fatal("a 15% failure rate must not open the breaker")
	}
	// Too few requests say nothing about the rate, even at 100% failures.
	cold := uuid.New()
	if _, opened := feed(breakers, clock, cold, 0, siteBreakerMinSamples-1); opened {
		t.Fatalf("%d failures are below the minimum sample size", siteBreakerMinSamples-1)
	}
}

func TestSiteBreakerOpensWhenMostRequestsFail(t *testing.T) {
	t.Parallel()

	breakers, clock := newTestSiteBreakers()
	site := uuid.New()
	opening, opened := feed(breakers, clock, site, 4, 10)
	if !opened {
		t.Fatal("8 of the first 12 requests failing should open the breaker")
	}
	if opening.Level != 1 || opening.Duration != siteBreakerBaseOpen || opening.Failures != 8 || opening.Total != 12 {
		t.Fatalf("opening = %#v, want level 1 for the base duration over 8/12 requests", opening)
	}
	if _, again := breakers.recordFailure(site); again {
		t.Fatal("a breaker that is already open must not reopen on every failure")
	}
}

func TestSiteBreakerProbationReopensEarlyAndBacksOff(t *testing.T) {
	t.Parallel()

	breakers, clock := newTestSiteBreakers()
	site := uuid.New()
	first, opened := feed(breakers, clock, site, 0, siteBreakerMinSamples)
	if !opened || first.Level != 1 {
		t.Fatalf("first opening = %#v opened=%v", first, opened)
	}
	clock.Advance(first.Duration)
	second, opened := feed(breakers, clock, site, 0, siteBreakerRetryMinSamples)
	if !opened || second.Level != 2 || second.Duration != 2*siteBreakerBaseOpen {
		t.Fatalf("second opening = %#v opened=%v, want a quick reopen at level 2 with double the time", second, opened)
	}
	last := second
	for i := 0; i < 6; i++ {
		clock.Advance(last.Duration + time.Second)
		if next, did := feed(breakers, clock, site, 0, siteBreakerRetryMinSamples); did {
			last = next
		}
	}
	if last.Duration != siteBreakerMaxOpen || last.Level != siteBreakerMaxLevel {
		t.Fatalf("last opening = %#v, want the cap of %s at level %d", last, siteBreakerMaxOpen, siteBreakerMaxLevel)
	}
}

func TestSiteBreakerForgetsOldFailuresAndLevelAfterQuietPeriod(t *testing.T) {
	t.Parallel()

	breakers, clock := newTestSiteBreakers()
	site := uuid.New()
	feed(breakers, clock, site, 0, siteBreakerMinSamples-1)
	clock.Advance(6 * time.Minute) // older than the window
	if _, opened := feed(breakers, clock, site, 0, 1); opened {
		t.Fatal("failures older than the window must not count")
	}

	if _, opened := feed(breakers, clock, site, 0, siteBreakerMinSamples); !opened {
		t.Fatal("expected an opening to build up level")
	}
	clock.Advance(siteBreakerProbation + time.Minute)
	opening, opened := feed(breakers, clock, site, 0, siteBreakerMinSamples)
	if !opened || opening.Level != 1 {
		t.Fatalf("opening after a quiet probation = %#v opened=%v, want the level back at 1", opening, opened)
	}
}

func TestSiteBreakerSuccessThroughDemotedSiteClosesIt(t *testing.T) {
	t.Parallel()

	breakers, clock := newTestSiteBreakers()
	site := uuid.New()
	if _, opened := feed(breakers, clock, site, 0, siteBreakerMinSamples); !opened {
		t.Fatal("expected the breaker to open")
	}
	breakers.recordSuccess(site, true)
	if _, opened := feed(breakers, clock, site, 0, siteBreakerRetryMinSamples); !opened {
		t.Fatal("after the site proved itself, renewed failures must be able to reopen the breaker")
	}
}

func TestSiteBreakerIsNilSafe(t *testing.T) {
	t.Parallel()

	var breakers *siteBreakers
	breakers.recordSuccess(uuid.New(), true)
	if _, opened := breakers.recordFailure(uuid.New()); opened {
		t.Fatal("a nil breaker never opens")
	}
	Handler{}.noteSiteSuccess(routeengineCandidateForTest())
}

func TestSiteAttributableFailure(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct {
		result gatewayAttemptResult
		want   bool
	}{
		"timeout":                {gatewayAttemptResult{errorType: "upstream_timeout", statusCode: 502}, true},
		"transport error":        {gatewayAttemptResult{errorType: "upstream_transport_error", statusCode: 502}, true},
		"upstream 502":           {gatewayAttemptResult{errorType: "upstream_http_error", statusCode: 502, upstreamStatusCode: 502}, true},
		"upstream 503":           {gatewayAttemptResult{errorType: "upstream_http_error", statusCode: 503, upstreamStatusCode: 503}, true},
		"first output timeout":   {gatewayAttemptResult{errorType: upstreamFirstOutputTimeoutErrorType, statusCode: 502}, true},
		"stream broke":           {gatewayAttemptResult{errorType: "upstream_stream_eof", statusCode: 200, responseStarted: true}, true},
		"transient 4xx":          {gatewayAttemptResult{errorType: upstreamTransientErrorType, statusCode: 408}, true},
		"bad request":            {gatewayAttemptResult{errorType: "upstream_http_error", statusCode: 400, upstreamStatusCode: 400}, false},
		"not found":              {gatewayAttemptResult{errorType: "upstream_http_error", statusCode: 404, upstreamStatusCode: 404}, false},
		"key rate limited":       {gatewayAttemptResult{errorType: "upstream_credential_limited", statusCode: 429, upstreamStatusCode: 429}, false},
		"key invalid":            {gatewayAttemptResult{errorType: "upstream_credential_invalid", statusCode: http.StatusBadGateway}, false},
		"client cancelled":       {gatewayAttemptResult{errorType: "downstream_client_cancelled", statusCode: 499}, false},
		"concurrency limited":    {gatewayAttemptResult{errorType: "site_concurrency_limited", statusCode: 503}, false},
		"our own buffer limit":   {gatewayAttemptResult{errorType: "upstream_stream_preoutput_too_large", statusCode: 502}, false},
		"success":                {gatewayAttemptResult{success: true, errorType: "upstream_timeout"}, false},
		"semantic response fail": {gatewayAttemptResult{errorType: "upstream_response_failed", statusCode: 502}, false},
	} {
		if got := siteAttributableFailure(tt.result); got != tt.want {
			t.Errorf("%s: siteAttributableFailure() = %v, want %v", name, got, tt.want)
		}
	}
}

func TestSiteBreakerCooldownInputIsASiteScopedGatewayCooldown(t *testing.T) {
	t.Parallel()

	siteID := uuid.New()
	input := siteBreakerCooldownInput(siteID, siteBreakerOpening{Duration: 2 * time.Minute, Level: 2, Failures: 9, Total: 12})
	if input.SiteID != siteID || input.Scope != "site" || input.Source != "gateway" || input.SiteModelID != nil || input.SiteCredentialID != nil {
		t.Fatalf("input = %#v, want a site-scoped gateway cooldown", input)
	}
	if input.Reason != store.CooldownReasonUpstreamSiteBreaker || input.Duration != 2*time.Minute {
		t.Fatalf("reason/duration = %q/%s", input.Reason, input.Duration)
	}
	if !store.TransientRouteCooldown(store.RouteCooldown{SiteID: siteID, Source: input.Source, Reason: input.Reason}) {
		t.Fatal("the breaker cooldown must be soft so a sole candidate keeps serving")
	}
}

func routeengineCandidateForTest() routeengine.Candidate {
	return routeengine.Candidate{Site: routeengine.CandidateSite{ID: uuid.New()}}
}

func TestGatewayCooldownSetsKeepSiteBreakerModelsListed(t *testing.T) {
	t.Parallel()

	breakerSite, healthSite := uuid.New(), uuid.New()
	sites, _ := gatewayCooldownSets([]store.RouteCooldown{
		{SiteID: breakerSite, Source: "gateway", Reason: store.CooldownReasonUpstreamSiteBreaker},
		{SiteID: healthSite, Source: "site_health", Reason: "site health is unhealthy"},
	})
	if _, ok := sites[breakerSite]; ok {
		t.Fatal("a site breaker must not hide the site's models from the model list")
	}
	if _, ok := sites[healthSite]; !ok {
		t.Fatal("an unhealthy site stays hidden")
	}
}

func TestNoteSiteFailureOpensASiteScopedCooldownOnlyForSiteSideFailures(t *testing.T) {
	t.Parallel()

	var created []store.RouteCooldown
	db := softMappingGorm(t) // its fake driver supports the transaction ActivateCooldown opens
	if err := db.Callback().Create().Replace("gorm:create", func(tx *gorm.DB) {
		if item, ok := tx.Statement.Dest.(*store.RouteCooldown); ok {
			created = append(created, *item)
		}
		tx.Statement.RowsAffected = 1
	}); err != nil {
		t.Fatalf("replace create callback: %v", err)
	}
	if err := db.Callback().Update().Replace("gorm:update", func(tx *gorm.DB) { tx.Statement.RowsAffected = 1 }); err != nil {
		t.Fatalf("replace update callback: %v", err)
	}
	handler := Handler{
		router:  routeengine.NewService(gatewayStoreWithGorm(t, db)),
		logger:  gatewayDiscardLogger(),
		breaker: newSiteBreakers(),
	}
	candidate := routeengineCandidateForTest()

	// Key-level and request-level failures never open the breaker, however many.
	for i := 0; i < 3*siteBreakerMinSamples; i++ {
		handler.noteSiteFailure(t.Context(), candidate, gatewayAttemptResult{errorType: "upstream_credential_limited", statusCode: 429, upstreamStatusCode: 429})
		handler.noteSiteFailure(t.Context(), candidate, gatewayAttemptResult{errorType: "downstream_client_cancelled", statusCode: 499})
	}
	if len(created) != 0 {
		t.Fatalf("created %d cooldowns from failures that are not the site's fault", len(created))
	}

	for i := 0; i < siteBreakerMinSamples; i++ {
		handler.noteSiteFailure(t.Context(), candidate, gatewayAttemptResult{errorType: "upstream_http_error", statusCode: 502, upstreamStatusCode: 502})
	}
	if len(created) != 1 {
		t.Fatalf("created %d cooldowns, want exactly one site breaker", len(created))
	}
	got := created[0]
	if got.SiteID != candidate.Site.ID || got.SiteModelID.Valid || got.SiteCredentialID.Valid || got.Scope != "site" || got.Source != "gateway" || got.Reason != store.CooldownReasonUpstreamSiteBreaker {
		t.Fatalf("cooldown = %#v, want a site-wide gateway breaker", got)
	}
	if remaining := time.Until(got.ActiveUntil); remaining <= 0 || remaining > siteBreakerBaseOpen {
		t.Fatalf("breaker lasts %s, want up to %s", remaining, siteBreakerBaseOpen)
	}
}
