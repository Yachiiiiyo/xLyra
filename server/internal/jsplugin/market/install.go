package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
	appversion "xlyra/server/internal/version"
)

const (
	InstallCodeUnavailable        = "unavailable"
	InstallCodeRevocationsExpired = "revocations_expired"
	InstallCodeNotFound           = "not_found"
	InstallCodeVersionNotFound    = "version_not_found"
	InstallCodeIncompatible       = "incompatible"
	InstallCodeRevoked            = "revoked"
	InstallCodeDeveloperMismatch  = "developer_mismatch"
	InstallCodeDownloadFailed     = "download_failed"
	InstallCodeVerifyFailed       = "verify_failed"
	InstallCodePackageInvalid     = "package_invalid"
	InstallCodeUnsigned           = "unsigned"
	InstallCodeSelfTestFailed     = "selftest_failed"
	InstallCodeVersionConflict    = "version_conflict"
)

// InstallError is a failed market install. Code is stable for the HTTP layer.
// Reason is the compatibility reason. Proto is a marketproto error code.
type InstallError struct {
	Code   string
	Reason string
	Proto  string
	Err    error
}

func (e *InstallError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Code
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Proto != "" {
		msg += ": " + e.Proto
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *InstallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (s *Service) Install(ctx context.Context, adminID, pluginID, version string) (store.JSPluginVersion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeUnavailable}
	}
	cfg := s.cfg
	if !cfg.Enabled {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeUnavailable, Err: errors.New("plugin market is disabled")}
	}
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return store.JSPluginVersion{}, err
	}
	now := time.Now()
	snap, err := snapshotFrom(state, s.rootKeys, now)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeUnavailable, Err: err}
	}
	if snap.RevocationsExpired {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeRevocationsExpired}
	}
	if snap.Root == nil || snap.Index == nil || snap.Revocations == nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeUnavailable, Err: errors.New("market cache is incomplete")}
	}
	plugin, ok := findIndexedPlugin(snap.Index, pluginID)
	if !ok {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeNotFound}
	}
	selected, err := selectInstallVersion(plugin, version, appversion.Current().Version)
	if err != nil {
		return store.JSPluginVersion{}, err
	}
	if securityDisabled(snap.Revocations, plugin.ID, selected) {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeRevoked}
	}
	client, err := s.httpClient(cfg.ProxyID)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeDownloadFailed, Err: err}
	}
	raw, err := s.downloadPackage(ctx, client, s.installBases(cfg, state, snap.Root), selected.FileSHA256, selected.Size)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeDownloadFailed, Err: err}
	}
	att, err := marketproto.VerifyAttestation(selected.Attestation, snap.Root, now)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVerifyFailed, Proto: marketproto.Code(err), Err: err}
	}
	pkg, err := jsplugin.ReadPackage(raw, true)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodePackageInvalid, Err: err}
	}
	// 市场安装也属于发布环节，这里检查图标可读性。
	if _, err := jsplugin.CheckIconLegibility(raw, pkg.Manifest); err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodePackageInvalid, Err: err}
	}
	if strings.TrimSpace(pkg.Signer) == "" {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeUnsigned}
	}
	if !signerListed(pkg.Signer, plugin.Developer.Fingerprints) || !signerListed(att.DeveloperFingerprint, plugin.Developer.Fingerprints) {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeDeveloperMismatch}
	}
	if err := marketproto.CheckPackage(att, packageFacts(pkg), plugin.Developer.Fingerprints); err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVerifyFailed, Proto: marketproto.Code(err), Err: err}
	}
	if att.PluginID != plugin.ID || att.Version != selected.Version {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVerifyFailed, Proto: marketproto.CodeManifestMismatch}
	}
	compiled, err := jsplugin.CompilePackage(pkg)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodePackageInvalid, Err: err}
	}
	if err := compiled.SelfTest(ctx); err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeSelfTestFailed, Err: err}
	}
	return s.storeInstalled(ctx, adminID, raw, pkg, selected.Attestation)
}

func (s *Service) VerifyVersionAttestation(ctx context.Context, row store.JSPluginVersion) bool {
	if s == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root, revocations, err := s.trustView(ctx)
	if err != nil || root == nil {
		return false
	}
	att, err := marketproto.VerifyAttestationRaw(row.Attestation, root, time.Now())
	if err != nil {
		return false
	}
	if att.PackageDigest != row.PackageSHA256 || att.DeveloperFingerprint != row.Signer || att.PluginID != row.PluginID || att.Version != row.Version {
		return false
	}
	for _, effect := range marketproto.MatchRevocations(revocations, marketproto.Target{
		PluginID:          row.PluginID,
		Version:           row.Version,
		PackageDigest:     row.PackageSHA256,
		SignerFingerprint: row.Signer,
		Attested:          true,
	}) {
		if effect.Action == "disable" {
			return false
		}
	}
	return true
}

func (s *Service) trustView(ctx context.Context) (*marketproto.Root, *marketproto.Revocations, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	s.trustMu.Lock()
	if s.trustCached != nil && now.Sub(s.trustCached.at) < trustViewTTL {
		root, revocations := s.trustCached.root, s.trustCached.revocations
		s.trustMu.Unlock()
		return root, revocations, nil
	}
	s.trustMu.Unlock()

	state, err := s.repo.GetMarketTrustState(ctx)
	if err != nil {
		return nil, nil, err
	}
	root, revocations, err := verifyTrustState(state, s.rootKeys, time.Now())
	if err != nil {
		return nil, nil, err
	}
	s.trustMu.Lock()
	s.trustCached = &cachedTrust{at: time.Now(), root: root, revocations: revocations}
	s.trustMu.Unlock()
	return root, revocations, nil
}

func (s *Service) clearTrustView() {
	if s == nil {
		return
	}
	s.trustMu.Lock()
	s.trustCached = nil
	s.trustMu.Unlock()
}

func verifyTrustState(state store.JSPluginMarketState, rootKeys []ed25519.PublicKey, now time.Time) (*marketproto.Root, *marketproto.Revocations, error) {
	if len(state.RootEnvelope) == 0 {
		return nil, nil, fmt.Errorf("market cache is missing root")
	}
	root, err := marketproto.VerifyRoot(state.RootEnvelope, rootKeys, marketproto.State{
		Seq: state.RootSeq, PayloadSHA256: state.RootPayloadSHA256,
	}, now)
	if err != nil {
		return nil, nil, err
	}
	if len(state.RevocationsEnvelope) == 0 {
		return root, nil, nil
	}
	revocations, _, err := verifyRevocationsSnapshot(state, root, now)
	if err != nil {
		return nil, nil, err
	}
	return root, revocations, nil
}

func (s *Service) installBases(cfg config.PluginMarketConfig, state store.JSPluginMarketState, root *marketproto.Root) []string {
	if strings.TrimSpace(cfg.SourceURL) != "" {
		return candidateBases(cfg.SourceURL, nil, nil)
	}
	var cached []string
	if strings.TrimSpace(state.SourceURL) != "" {
		cached = append(cached, state.SourceURL)
	}
	if root != nil {
		cached = append(cached, root.Endpoints.Registry...)
	}
	return candidateBases("", cached, s.registries)
}

func (s *Service) downloadPackage(ctx context.Context, client *http.Client, bases []string, fileSHA string, size int64) ([]byte, error) {
	return downloadRelative(ctx, client, bases, "packages/"+fileSHA+".xlp", jsplugin.MaxPackageBytes, func(body []byte) error {
		return marketproto.CheckFile(body, fileSHA, size)
	})
}

func downloadRelative(ctx context.Context, client *http.Client, bases []string, rel string, maxBytes int, accept func([]byte) error) ([]byte, error) {
	if client == nil {
		return nil, errors.New("missing http client")
	}
	if len(bases) == 0 {
		return nil, errors.New("no market base url")
	}
	var last error
	for _, base := range bases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, _, _, err := fetch(ctx, client, documentURL(base, rel), "", maxBytes)
		if err != nil {
			last = err
			continue
		}
		if accept != nil {
			if err := accept(body); err != nil {
				last = err
				continue
			}
		}
		return body, nil
	}
	if last == nil {
		last = errors.New("download failed")
	}
	return nil, last
}

func (s *Service) storeInstalled(ctx context.Context, adminID string, raw []byte, pkg jsplugin.Package, attEnv marketproto.Envelope) (store.JSPluginVersion, error) {
	existing, err := s.repo.GetVersion(ctx, pkg.Manifest.ID, pkg.Manifest.Version)
	if err == nil {
		return s.reuseInstalled(ctx, pkg, attEnv, existing)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.JSPluginVersion{}, err
	}
	manifestJSON, err := json.Marshal(pkg.Manifest)
	if err != nil {
		return store.JSPluginVersion{}, err
	}
	attRaw, err := json.Marshal(attEnv)
	if err != nil {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVerifyFailed, Err: err}
	}
	selftest, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		return store.JSPluginVersion{}, err
	}
	row := store.JSPluginVersion{
		PluginID:      pkg.Manifest.ID,
		Version:       pkg.Manifest.Version,
		Manifest:      store.JSON(manifestJSON),
		Package:       raw,
		PackageSHA256: pkg.SHA256,
		Signer:        pkg.Signer,
		Status:        store.JSPluginStatusVerified,
		SelfTest:      store.JSON(selftest),
		Origin:        store.JSPluginOriginMarket,
		Attestation:   store.JSON(attRaw),
		UploadedBy:    adminUUID(adminID),
	}
	if err := s.repo.CreatePluginVersion(ctx, row, store.JSPluginSourceMarket); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return s.adoptDuplicate(ctx, pkg, attEnv)
		}
		return store.JSPluginVersion{}, err
	}
	stored, err := s.repo.GetVersion(ctx, row.PluginID, row.Version)
	if err != nil {
		return row, nil
	}
	return stored, nil
}

func (s *Service) adoptDuplicate(ctx context.Context, pkg jsplugin.Package, attEnv marketproto.Envelope) (store.JSPluginVersion, error) {
	existing, err := s.repo.GetVersion(ctx, pkg.Manifest.ID, pkg.Manifest.Version)
	if err != nil {
		return store.JSPluginVersion{}, err
	}
	return s.reuseInstalled(ctx, pkg, attEnv, existing)
}

func (s *Service) reuseInstalled(ctx context.Context, pkg jsplugin.Package, attEnv marketproto.Envelope, existing store.JSPluginVersion) (store.JSPluginVersion, error) {
	if existing.PackageSHA256 != pkg.SHA256 {
		return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVersionConflict, Err: jsplugin.ErrVersionContentMismatch}
	}
	if attestationMissing(existing.Attestation) {
		attRaw, err := json.Marshal(attEnv)
		if err != nil {
			return store.JSPluginVersion{}, &InstallError{Code: InstallCodeVerifyFailed, Err: err}
		}
		if err := s.repo.UpdateVersionAttestation(ctx, pkg.Manifest.ID, pkg.Manifest.Version, store.JSPluginOriginMarket, store.JSON(attRaw)); err != nil {
			return store.JSPluginVersion{}, err
		}
		return s.repo.GetVersion(ctx, pkg.Manifest.ID, pkg.Manifest.Version)
	}
	return existing, nil
}

func findIndexedPlugin(idx *marketproto.Index, id string) (marketproto.Plugin, bool) {
	id = strings.TrimSpace(id)
	if idx == nil || id == "" {
		return marketproto.Plugin{}, false
	}
	for _, plugin := range idx.Plugins {
		if plugin.ID == id {
			return plugin, true
		}
	}
	return marketproto.Plugin{}, false
}

func selectInstallVersion(plugin marketproto.Plugin, want, xlyra string) (marketproto.PluginVersion, error) {
	versions := append([]marketproto.PluginVersion(nil), plugin.Versions...)
	sortVersionsNewestFirst(versions)
	want = strings.TrimSpace(want)
	if want == "" {
		reason := ""
		for i, ver := range versions {
			next := incompatibleReason(ver.Compat, xlyra)
			if i == 0 {
				reason = next
			}
			if next == "" {
				return ver, nil
			}
		}
		if len(versions) == 0 {
			return marketproto.PluginVersion{}, &InstallError{Code: InstallCodeVersionNotFound}
		}
		return marketproto.PluginVersion{}, &InstallError{Code: InstallCodeIncompatible, Reason: reason}
	}
	for _, ver := range versions {
		if ver.Version != want {
			continue
		}
		if reason := incompatibleReason(ver.Compat, xlyra); reason != "" {
			return marketproto.PluginVersion{}, &InstallError{Code: InstallCodeIncompatible, Reason: reason}
		}
		return ver, nil
	}
	return marketproto.PluginVersion{}, &InstallError{Code: InstallCodeVersionNotFound}
}

func securityDisabled(rev *marketproto.Revocations, pluginID string, ver marketproto.PluginVersion) bool {
	for _, effect := range marketproto.MatchRevocations(rev, marketproto.Target{
		PluginID:      pluginID,
		Version:       ver.Version,
		PackageDigest: ver.PackageDigest,
		Attested:      true,
	}) {
		if effect.Action == "disable" {
			return true
		}
	}
	return false
}

func signerListed(fingerprint string, fingerprints []string) bool {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return false
	}
	for _, item := range fingerprints {
		if item == fingerprint {
			return true
		}
	}
	return false
}

func packageFacts(pkg jsplugin.Package) marketproto.PackageFacts {
	return marketproto.PackageFacts{
		PackageDigest:     pkg.SHA256,
		SignerFingerprint: pkg.Signer,
		PluginID:          pkg.Manifest.ID,
		Version:           pkg.Manifest.Version,
		Kind:              pkg.Manifest.Kind,
		Hosts:             sortedSet(pkg.Manifest.SiteProvider.Hosts),
		Permissions:       sortedSet(pkg.Manifest.Automation.Permissions),
	}
}

func sortedSet(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func attestationMissing(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

func adminUUID(adminID string) uuid.NullUUID {
	id, err := uuid.Parse(strings.TrimSpace(adminID))
	if err != nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: id, Valid: true}
}
