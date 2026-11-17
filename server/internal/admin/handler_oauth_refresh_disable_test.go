package admin

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	oauthsvc "xlyra/server/internal/oauth"
	"xlyra/server/internal/site"
	"xlyra/server/internal/store"
)

func TestOAuthRefreshAuthFailureClassificationKeepsRateLimitedSitesEnabled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider string
		message  string
		want     bool
	}{
		{
			name:     "claude code usage rate limited",
			provider: "claude_code",
			message:  `all 1 enabled api keys failed: claude code usage returned 429: {   "error": {     "type": "rate_limit_error",     "message": "Rate limited. Please try again later."   } }`,
			want:     false,
		},
		{
			name:     "upstream bad gateway",
			provider: "claude_code",
			message:  "all 1 enabled api keys failed: claude code usage returned 502: bad gateway",
			want:     false,
		},
		{
			name:     "claude code unauthorized",
			provider: "claude_code",
			message:  "all 1 enabled api keys failed: claude code usage returned 401: invalid token",
			want:     true,
		},
		{
			name:     "codex invalid grant",
			provider: "codex",
			message:  "refresh failed: invalid_grant",
			want:     true,
		},
	}
	for _, tc := range cases {
		if got := site.OAuthPermanentAuthRefreshError(tc.provider, tc.message); got != tc.want {
			t.Fatalf("%s: OAuthPermanentAuthRefreshError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMarkOAuthConnectionUnavailableOnRefreshAuthFailureWithoutOAuthServiceIsNoop(t *testing.T) {
	t.Parallel()

	(Handler{}).markOAuthConnectionUnavailableOnRefreshAuthFailure(t.Context(), [16]byte{}, "claude_code", assertionError("claude code usage returned 401"))
}

// A site refresh that fails on a rate limit or an upstream outage must leave the
// connection connected and its site enabled; only a permanent auth failure marks
// the connection for reconnect and disables the site.
func TestMarkOAuthConnectionUnavailableOnRefreshAuthFailureOnlyDisablesOnPermanentAuthErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		provider    string
		message     string
		wantDisable bool
	}{
		{
			name:     "claude code usage rate limited",
			provider: "claude_code",
			message:  `all 1 enabled api keys failed: claude code usage returned 429: {"error":{"type":"rate_limit_error","message":"Rate limited. Please try again later."}}`,
		},
		{
			name:     "codex upstream bad gateway",
			provider: "codex",
			message:  "all 1 enabled api keys failed: codex usage returned 502: bad gateway",
		},
		{
			name:        "claude code unauthorized",
			provider:    "claude_code",
			message:     "all 1 enabled api keys failed: claude code usage returned 401: invalid token",
			wantDisable: true,
		},
		{
			name:        "codex invalid grant",
			provider:    "codex",
			message:     "refresh failed: invalid_grant",
			wantDisable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			connectionID := uuid.New()
			siteID := uuid.New()
			var savedConnection *store.OAuthConnection
			var savedSite *store.Site
			db := adminStoreWithCallbacks(t, adminGormCallbacks{
				query: func(tx *gorm.DB) {
					switch dest := tx.Statement.Dest.(type) {
					case *store.OAuthConnection:
						*dest = store.OAuthConnection{ID: connectionID, Provider: tc.provider, Status: "connected", SiteID: &siteID}
					case *store.Site:
						*dest = store.Site{ID: siteID, SiteType: tc.provider, Status: "active", Enabled: true}
					default:
						tx.AddError(fmt.Errorf("unexpected query destination %T", tx.Statement.Dest))
						return
					}
					tx.Statement.RowsAffected = 1
				},
				update: func(tx *gorm.DB) {
					switch dest := tx.Statement.Dest.(type) {
					case *store.OAuthConnection:
						saved := *dest
						savedConnection = &saved
					case *store.Site:
						saved := *dest
						savedSite = &saved
					default:
						tx.AddError(fmt.Errorf("unexpected update destination %T", tx.Statement.Dest))
						return
					}
					tx.Statement.RowsAffected = 1
				},
			})
			handler := Handler{oauth: oauthsvc.NewService(db, adminTestMasterKey)}

			handler.markOAuthConnectionUnavailableOnRefreshAuthFailure(t.Context(), connectionID, tc.provider, assertionError(tc.message))

			if !tc.wantDisable {
				if savedConnection != nil || savedSite != nil {
					t.Fatalf("connection = %+v, site = %+v; want both left untouched", savedConnection, savedSite)
				}
				return
			}
			if savedConnection == nil || savedConnection.Status != "reconnect_required" {
				t.Fatalf("connection = %+v, want status reconnect_required", savedConnection)
			}
			if savedSite == nil || savedSite.Enabled {
				t.Fatalf("site = %+v, want disabled", savedSite)
			}
		})
	}
}
