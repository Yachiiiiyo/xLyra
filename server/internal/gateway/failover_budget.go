package gateway

import (
	"context"
	"time"
)

const (
	defaultGatewayFailoverBudget   = 300 * time.Second
	defaultStreamingFailoverBudget = 90 * time.Second
	defaultImageFailoverBudget     = 180 * time.Second
)

func gatewayFailoverBudget(request gatewayRequest) time.Duration {
	if request.Stream {
		if request.DownstreamPath == gatewayEndpointImagesGenerations || request.DownstreamPath == gatewayEndpointImagesEdits {
			return defaultImageFailoverBudget
		}
		return defaultStreamingFailoverBudget
	}
	return defaultGatewayFailoverBudget
}

// maxCredentialAttemptsPerSite caps how many keys of one site a single request
// walks through. Every failed key is cooled down, so a pool of dead keys is
// drained across requests instead of stalling one request on all of them.
const maxCredentialAttemptsPerSite = 8

type failoverDeadlineContextKey struct{}

func withFailoverDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, failoverDeadlineContextKey{}, deadline)
}

// failoverBudgetExhausted reports whether the request's failover deadline has
// passed. A context without a deadline never reports exhaustion.
func failoverBudgetExhausted(ctx context.Context) bool {
	deadline, ok := ctx.Value(failoverDeadlineContextKey{}).(time.Time)
	return ok && !time.Now().Before(deadline)
}
