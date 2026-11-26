package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	routeengine "xlyra/server/internal/router"
)

type routePickContextKey struct{}

func withRoutePick(ctx context.Context, pick routeengine.RoutePick) context.Context {
	return context.WithValue(ctx, routePickContextKey{}, pick)
}

// routePickMetadata is recorded on every attempt so a request log shows whether
// the lead candidate won on priority alone or was picked among equals.
func routePickMetadata(ctx context.Context) any {
	pick, ok := ctx.Value(routePickContextKey{}).(routeengine.RoutePick)
	if !ok || pick.Mode == "" {
		return nil
	}
	return map[string]any{"mode": pick.Mode, "tier_size": pick.TierSize}
}

// routeAffinitySeed identifies the conversation a request belongs to, so that
// equal-priority candidates are spread across conversations while every turn of
// one conversation keeps going to the same site and its prompt cache. An
// explicit session hint wins; otherwise the system prompt, tools and first
// message identify the conversation. An empty seed (nothing identifying) means
// the choice is random.
func routeAffinitySeed(request gatewayRequest) string {
	if hint := gatewayRequestConversationHint(request); hint != "" {
		return "hint:" + hint
	}
	if key := strings.TrimSpace(anyString(request.Payload["prompt_cache_key"])); key != "" {
		return "cache-key:" + key
	}
	if root, messages, ok := cacheObservationCanonicalLineage(request); ok {
		sum := sha256.New()
		sum.Write(root)
		if len(messages) > 0 {
			sum.Write(messages[0])
		}
		return "lineage:" + hex.EncodeToString(sum.Sum(nil))
	}
	return ""
}
