package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
)

// The site breaker watches the real traffic of each site. The scheduled health
// probe only fetches /v1/models every 15 minutes and needs three failures in a
// row, so a site that answers the probe but fails real requests stayed ranked
// first for as long as an hour. When most recent requests to a site failed on
// the site's side, the breaker demotes the whole site (a soft cooldown: it is
// tried last, never removed, so a key whose only candidate is demoted keeps
// working). It stays closed for healthy and merely noisy sites.
const (
	siteBreakerBucket  = 10 * time.Second
	siteBreakerBuckets = 30 // a five minute window

	// A cold site needs this many requests in the window before its failure rate
	// means anything.
	siteBreakerMinSamples = 12
	// Right after a breaker closed the site is on probation: fewer samples are
	// enough to open it again, and the open time grows.
	siteBreakerRetryMinSamples = 6
	siteBreakerFailureRate     = 0.6

	siteBreakerBaseOpen  = time.Minute
	siteBreakerMaxOpen   = 5 * time.Minute
	siteBreakerMaxLevel  = 4
	siteBreakerProbation = 30 * time.Minute
)

type breakerBucket struct {
	slot int64
	ok   int
	fail int
}

type siteBreakerState struct {
	buckets    [siteBreakerBuckets]breakerBucket
	level      int
	lastOpened time.Time
	openUntil  time.Time
}

type siteBreakerOpening struct {
	Duration time.Duration
	Level    int
	Failures int
	Total    int
}

type siteBreakers struct {
	mu    sync.Mutex
	sites map[uuid.UUID]*siteBreakerState
	now   func() time.Time
}

func newSiteBreakers() *siteBreakers {
	return &siteBreakers{sites: map[uuid.UUID]*siteBreakerState{}, now: time.Now}
}

func (b *siteBreakers) state(siteID uuid.UUID) *siteBreakerState {
	state, ok := b.sites[siteID]
	if !ok {
		state = &siteBreakerState{}
		b.sites[siteID] = state
	}
	return state
}

func (b *siteBreakers) bucketFor(state *siteBreakerState, now time.Time) (*breakerBucket, int64) {
	slot := now.Unix() / int64(siteBreakerBucket/time.Second)
	bucket := &state.buckets[slot%siteBreakerBuckets]
	if bucket.slot != slot {
		*bucket = breakerBucket{slot: slot}
	}
	return bucket, slot
}

// recordSuccess counts a served request. resetOpen reports that the request
// went through a demoted site, which proves the site answers again.
func (b *siteBreakers) recordSuccess(siteID uuid.UUID, resetOpen bool) {
	if b == nil || siteID == uuid.Nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.state(siteID)
	bucket, _ := b.bucketFor(state, b.now())
	bucket.ok++
	if resetOpen {
		state.openUntil = time.Time{}
		state.clearWindow()
	}
}

// recordFailure counts a site-side failure and reports whether the breaker
// opens because of it.
func (b *siteBreakers) recordFailure(siteID uuid.UUID) (siteBreakerOpening, bool) {
	if b == nil || siteID == uuid.Nil {
		return siteBreakerOpening{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	state := b.state(siteID)
	bucket, slot := b.bucketFor(state, now)
	bucket.fail++
	if now.Before(state.openUntil) {
		return siteBreakerOpening{}, false
	}
	if state.level > 0 && now.Sub(state.lastOpened) >= siteBreakerProbation {
		state.level = 0
	}
	minSamples := siteBreakerMinSamples
	if state.level > 0 {
		minSamples = siteBreakerRetryMinSamples
	}
	okCount, failCount := 0, 0
	for _, item := range state.buckets {
		if item.slot > slot-siteBreakerBuckets && item.slot <= slot {
			okCount += item.ok
			failCount += item.fail
		}
	}
	total := okCount + failCount
	if total < minSamples || float64(failCount)/float64(total) < siteBreakerFailureRate {
		return siteBreakerOpening{}, false
	}
	if state.level < siteBreakerMaxLevel {
		state.level++
	}
	duration := siteBreakerBaseOpen << (state.level - 1)
	if duration > siteBreakerMaxOpen {
		duration = siteBreakerMaxOpen
	}
	state.openUntil = now.Add(duration)
	state.lastOpened = now
	state.clearWindow()
	return siteBreakerOpening{Duration: duration, Level: state.level, Failures: failCount, Total: total}, true
}

func (s *siteBreakerState) clearWindow() {
	s.buckets = [siteBreakerBuckets]breakerBucket{}
}

// siteAttributableFailure reports a failed attempt that says something about
// the site itself. Request mistakes, client hang-ups and key-level problems
// (auth, balance, rate limit, concurrency) already have their own handling and
// must not demote a healthy site.
func siteAttributableFailure(result gatewayAttemptResult) bool {
	if result.success {
		return false
	}
	switch result.errorType {
	case "upstream_timeout", "upstream_transport_error", upstreamFirstOutputTimeoutErrorType, upstreamTransientErrorType:
		return true
	case "upstream_http_error":
		return result.upstreamStatusCode >= http.StatusInternalServerError
	}
	// A stream that breaks after output started is the site's doing too, but a
	// pre-output buffer overflow is our own limit.
	return upstreamStreamFailure(result) && result.errorType != "upstream_stream_preoutput_too_large"
}

func siteBreakerCooldownInput(siteID uuid.UUID, opening siteBreakerOpening) routeengine.CooldownInput {
	return routeengine.CooldownInput{
		SiteID:   siteID,
		Scope:    "site",
		Source:   "gateway",
		Reason:   store.CooldownReasonUpstreamSiteBreaker,
		Duration: opening.Duration,
		Metadata: map[string]any{
			"failures":         opening.Failures,
			"requests":         opening.Total,
			"level":            opening.Level,
			"window_secs":      int(siteBreakerBucket/time.Second) * siteBreakerBuckets,
			"min_failure_rate": siteBreakerFailureRate,
		},
	}
}

func (h Handler) noteSiteFailure(ctx context.Context, candidate routeengine.Candidate, result gatewayAttemptResult) {
	if h.breaker == nil || h.router == nil || !siteAttributableFailure(result) {
		return
	}
	opening, opened := h.breaker.recordFailure(candidate.Site.ID)
	if !opened {
		return
	}
	if _, err := h.router.ActivateCooldown(ctx, siteBreakerCooldownInput(candidate.Site.ID, opening)); err != nil {
		h.logger.WarnContext(ctx, "failed to activate site breaker", "scope", "gateway", "site_id", candidate.Site.ID, "error", err)
		return
	}
	h.logger.WarnContext(ctx, "site breaker opened: most recent requests failed on the site side",
		"scope", "gateway", "site_id", candidate.Site.ID, "site_name", candidate.Site.Name,
		"failures", opening.Failures, "requests", opening.Total, "level", opening.Level, "demoted_for", opening.Duration.String())
	h.InvalidateModelsCache()
}

func (h Handler) noteSiteSuccess(candidate routeengine.Candidate) {
	h.breaker.recordSuccess(candidate.Site.ID, candidate.Cooling && candidate.CoolingReason == store.CooldownReasonUpstreamSiteBreaker)
}
