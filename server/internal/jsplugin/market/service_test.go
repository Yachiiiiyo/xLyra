package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
	"xlyra/server/internal/version"
)

func TestCandidateBases(t *testing.T) {
	cached := []string{"https://cached.example/v1/", "https://fallback.example/v1/"}
	fallbacks := []string{"https://fallback.example/v1/", "https://builtin.example/v1/"}
	if got := candidateBases(" https://mirror.example/v1/ ", cached, fallbacks); len(got) != 1 || got[0] != "https://mirror.example/v1/" {
		t.Fatalf("source url = %#v", got)
	}
	got := candidateBases("", cached, fallbacks)
	want := []string{"https://cached.example/v1/", "https://fallback.example/v1/", "https://builtin.example/v1/"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bases = %#v", got)
	}
	if got := candidateBases("  ", nil, fallbacks); strings.Join(got, ",") != "https://fallback.example/v1/,https://builtin.example/v1/" {
		t.Fatalf("fallbacks = %#v", got)
	}
}

func TestIndexDue(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	old := now.Add(-7 * time.Hour)
	fresh := store.JSPluginMarketState{IndexSyncedAt: &recent, IndexEnvelope: []byte{1}}
	if indexDue(fresh, 6, false, now) {
		t.Fatal("fresh index should wait")
	}
	if !indexDue(fresh, 6, true, now) {
		t.Fatal("force should refresh")
	}
	stale := fresh
	stale.IndexSyncedAt = &old
	if !indexDue(stale, 6, false, now) {
		t.Fatal("stale index should refresh")
	}
	if !indexDue(store.JSPluginMarketState{}, 6, false, now) {
		t.Fatal("never synced should refresh")
	}
	missing := fresh
	missing.IndexEnvelope = nil
	if !indexDue(missing, 6, false, now) {
		t.Fatal("missing envelope should refresh")
	}
}

func TestSyncOnceFreshThenNotModified(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	docs := signDocs(t, rootPriv, online, 1, 2, 3, gen, exp)
	cloud := newCloud(docs)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{"https://unused.example/v1/"}, conf)

	ctx := context.Background()
	empty, err := svc.Snapshot(ctx)
	if err != nil || empty.Root != nil || empty.Index != nil || empty.Revocations != nil || empty.IndexExpired || empty.RevocationsExpired {
		t.Fatalf("empty snapshot = %+v, %v", empty, err)
	}
	status, err := svc.SyncOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Error != "" || status.SourceURL != base || status.Root.NotModified || status.Index.NotModified || status.Revocations.NotModified {
		t.Fatalf("first status = %+v", status)
	}
	if status.Root.Seq != 1 || status.Index.Seq != 2 || status.Revocations.Seq != 3 {
		t.Fatalf("seqs = %+v %+v %+v", status.Root, status.Index, status.Revocations)
	}
	first, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceURL != base || first.LastError != "" || first.RootSeq != 1 || first.IndexSeq != 2 || first.RevocationsSeq != 3 {
		t.Fatalf("state = %+v", first)
	}
	if first.IndexSyncedAt == nil || first.RevocationsSyncedAt == nil || first.LastAttemptAt == nil || first.LeaseOwner != "" {
		t.Fatalf("times or lease = %+v", first)
	}
	if len(first.RootEnvelope) == 0 || first.RootETag == "" || first.RootPayloadSHA256 == "" {
		t.Fatalf("root cache = %+v", first)
	}
	snap, err := svc.Snapshot(ctx)
	if err != nil || snap.Root == nil || snap.Index == nil || snap.Revocations == nil || snap.IndexExpired || snap.RevocationsExpired {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if snap.Root.Seq != 1 || snap.Index.Seq != 2 || snap.Revocations.Seq != 3 {
		t.Fatalf("snapshot seqs root=%d index=%d revocations=%d", snap.Root.Seq, snap.Index.Seq, snap.Revocations.Seq)
	}
	assertAnonymous(t, cloud.headers, svc.owner)

	before := cloud.hit("index.json")
	status, err = svc.SyncOnce(ctx, false)
	if err != nil || status.Error != "" {
		t.Fatalf("second sync = %+v, %v", status, err)
	}
	if !status.Root.NotModified || !status.Revocations.NotModified || status.Index.NotModified {
		t.Fatalf("second status modified flags = %+v", status)
	}
	if cloud.hit("index.json") != before {
		t.Fatalf("index fetched again: %d -> %d", before, cloud.hit("index.json"))
	}
	second, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCachedDocs(first, second) || second.LastError != "" {
		t.Fatalf("cache changed:\nfirst=%+v\nsecond=%+v", first, second)
	}
	for _, hdr := range cloud.headers[len(cloud.headers)-cloud.hit("root.json"):] {
		if hdr.Get("If-None-Match") == "" {
			t.Fatal("second sync missing If-None-Match")
		}
	}
}

func TestSyncOnceSkipsFreshIndexUnlessForced(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	cloud := newCloud(signDocs(t, rootPriv, online, 1, 4, 1, gen, exp))
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, conf)
	ctx := context.Background()
	if _, err := svc.SyncOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	indexHits := cloud.hit("index.json")
	rootHits := cloud.hit("root.json")
	if _, err := svc.SyncOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	if cloud.hit("index.json") != indexHits {
		t.Fatalf("index hits = %d, want %d", cloud.hit("index.json"), indexHits)
	}
	if cloud.hit("root.json") <= rootHits {
		t.Fatal("root was not fetched on the second sync")
	}
	if _, err := svc.SyncOnce(ctx, true); err != nil {
		t.Fatal(err)
	}
	if cloud.hit("index.json") != indexHits+1 {
		t.Fatalf("forced index hits = %d, want %d", cloud.hit("index.json"), indexHits+1)
	}
}

func TestSyncOnceKeepsCacheOnSeqRollback(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	cloud := newCloud(signDocs(t, rootPriv, online, 2, 5, 2, gen, exp))
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, conf)
	ctx := context.Background()
	if _, err := svc.SyncOnce(ctx, true); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cloud.use(signDocs(t, rootPriv, online, 2, 4, 2, gen, exp))
	status, err := svc.SyncOnce(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Error, marketproto.CodeSeqRollback) {
		t.Fatalf("status error = %q", status.Error)
	}
	after, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCachedDocs(before, after) || !strings.Contains(after.LastError, marketproto.CodeSeqRollback) {
		t.Fatalf("cache or last_error = %+v", after)
	}
}

func TestSyncOnceFallsBackToNextBase(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	good := newCloud(signDocs(t, rootPriv, online, 1, 1, 1, gen, exp))
	goodSrv := httptest.NewServer(good)
	t.Cleanup(goodSrv.Close)
	badHits := 0
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(badSrv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{badSrv.URL + "/", goodSrv.URL + "/"}, nil)
	svc.skipEndpointBinding = true
	status, err := svc.SyncOnce(context.Background(), false)
	if err != nil || status.Error != "" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	state, err := repo.GetMarketState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SourceURL != goodSrv.URL+"/" || badHits == 0 || good.hit("root.json") == 0 {
		t.Fatalf("source = %q badHits=%d goodRoot=%d", state.SourceURL, badHits, good.hit("root.json"))
	}
}

func TestSyncOnceSourceURLDoesNotFallBack(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	good := newCloud(signDocs(t, rootPriv, online, 1, 1, 1, gen, exp))
	goodSrv := httptest.NewServer(good)
	t.Cleanup(goodSrv.Close)
	badHits := 0
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(badSrv.Close)
	base := badSrv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{goodSrv.URL + "/"}, conf)
	status, err := svc.SyncOnce(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if status.Error == "" || badHits == 0 || good.totalHits() != 0 {
		t.Fatalf("status=%q badHits=%d goodHits=%d", status.Error, badHits, good.totalHits())
	}
	state, err := repo.GetMarketState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.RootEnvelope) != 0 || state.SourceURL != "" || state.LastError == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestSyncOnceSingleFlightLease(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	cloud := newCloud(signDocs(t, rootPriv, online, 1, 1, 1, gen, exp))
	cloud.holdFirst = true
	cloud.entered = make(chan struct{})
	cloud.release = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(cloud.release) }) }
	t.Cleanup(release)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	keys := []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	a := newTestService(t, repo, keys, nil, conf)
	b := newTestService(t, repo, keys, nil, conf)
	if a.owner == b.owner {
		t.Fatal("owners collided")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type result struct {
		status Status
		err    error
	}
	results := make(chan result, 2)
	go func() {
		status, err := a.SyncOnce(ctx, false)
		results <- result{status, err}
	}()
	go func() {
		status, err := b.SyncOnce(ctx, false)
		results <- result{status, err}
	}()
	gotResult := false
	gotRequest := false
	var first result
	timeout := time.After(15 * time.Second)
	for !gotResult || !gotRequest {
		select {
		case first = <-results:
			gotResult = true
		case <-cloud.entered:
			gotRequest = true
		case <-timeout:
			t.Fatalf("timeout result=%v request=%v hits=%d", gotResult, gotRequest, cloud.totalHits())
		}
	}
	if cloud.totalHits() != 1 {
		t.Fatalf("in-flight requests = %d, want 1", cloud.totalHits())
	}
	if first.err != nil || first.status.Error != errBusy {
		t.Fatalf("first result = %+v, %v", first.status, first.err)
	}
	release()
	second := <-results
	if second.err != nil || second.status.Error != "" || second.status.SourceURL != base {
		t.Fatalf("winner = %+v, %v", second.status, second.err)
	}
	if cloud.totalHits() != 3 {
		t.Fatalf("total requests = %d, want 3", cloud.totalHits())
	}
}

func TestSyncOnceDisabledSkipsNetwork(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	cloud := newCloud(signDocs(t, rootPriv, online, 1, 1, 1, gen, exp))
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	conf := marketConf(t, config.PluginMarketConfig{Enabled: false, IndexSyncHours: 6, SourceURL: srv.URL + "/", AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{srv.URL + "/"}, conf)
	status, err := svc.SyncOnce(context.Background(), true)
	if err != nil || status.Error != "" || cloud.totalHits() != 0 {
		t.Fatalf("status=%+v err=%v hits=%d", status, err, cloud.totalHits())
	}
	state, err := repo.GetMarketState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptAt != nil || state.LastError != "" {
		t.Fatalf("disabled sync wrote state: %+v", state)
	}
}

func TestBuiltinRootKeys(t *testing.T) {
	keys, err := parseRootKeys(BuiltinRootKeys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"SHA256:/q6wZqVA1H+3lZK3i8+QRBGYXXm0k09Ph5IRIhszvNc=",
		"SHA256:Gs291BrEw+x6TAusI3/IEfXqGpB72zTp8qIeF4PYtEg=",
	}
	if len(keys) != len(want) {
		t.Fatalf("got %d built-in root keys, want %d", len(keys), len(want))
	}
	for i, key := range keys {
		if got := marketproto.KeyID(key); got != want[i] {
			t.Fatalf("root key %d keyId = %s, want %s", i, got, want[i])
		}
	}
}

func TestSyncOnceWithoutRootKeys(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{}, []string{srv.URL + "/"}, nil)
	status, err := svc.SyncOnce(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), errNoRootKeys) || status.Error != errNoRootKeys || hits != 0 {
		t.Fatalf("status=%+v err=%v hits=%d", status, err, hits)
	}
	state, err := repo.GetMarketState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.LastError != errNoRootKeys || state.LastAttemptAt == nil {
		t.Fatalf("state = %+v", state)
	}
}

func TestRootCoversBase(t *testing.T) {
	root := &marketproto.Root{Endpoints: marketproto.Endpoints{Registry: []string{
		" HTTPS://Registry.Example/v1/ ",
		"https://other.example/v1",
	}}}
	cases := []struct {
		name string
		base string
		ok   bool
	}{
		{name: "hit", base: "https://registry.example/v1/", ok: true},
		{name: "miss", base: "https://missing.example/v1/", ok: false},
		{name: "host case", base: "https://REGISTRY.example/v1/", ok: true},
		{name: "no trailing slash", base: "https://registry.example/v1", ok: true},
		{name: "extra trailing slash", base: "https://registry.example/v1///", ok: true},
		{name: "second endpoint", base: "https://OTHER.Example/v1/", ok: true},
		{name: "path case", base: "https://registry.example/V1/", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootCoversBase(root, tc.base); got != tc.ok {
				t.Fatalf("rootCoversBase(%q) = %v, want %v", tc.base, got, tc.ok)
			}
		})
	}
	if rootCoversBase(&marketproto.Root{}, "https://registry.example/v1/") {
		t.Fatal("empty registry list should miss")
	}
	if rootCoversBase(nil, "https://registry.example/v1/") {
		t.Fatal("nil root should miss")
	}
	migrated := &marketproto.Root{Endpoints: marketproto.Endpoints{Registry: []string{
		"https://old.example/v1/",
		"https://new.example/v1/",
	}}}
	for _, base := range []string{"https://old.example/v1", "HTTPS://New.Example/v1/"} {
		if !rootCoversBase(migrated, base) {
			t.Fatalf("migrated root should cover %s", base)
		}
	}
}

func TestSyncOnceRejectsUnboundRootAndTriesNextBase(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	cachedDocs := signDocsRegistries(t, rootPriv, online, 4, 5, 6, gen, exp, []string{"https://127.0.0.1:1/v1/"})
	before := stateFromDocs(t, cachedDocs, 4, 5, 6)
	before.SourceURL = "https://127.0.0.1:1/v1/"
	ctx := context.Background()
	if err := repo.SaveMarketState(ctx, before); err != nil {
		t.Fatal(err)
	}
	foreign := []string{"https://foreign.example/v1/"}
	first := newCloud(signDocsRegistries(t, rootPriv, online, 5, 1, 1, gen, exp, foreign))
	firstSrv := httptest.NewServer(first)
	t.Cleanup(firstSrv.Close)
	secondHits := 0
	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(secondSrv.Close)
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{firstSrv.URL + "/", secondSrv.URL + "/"}, nil)
	status, err := svc.SyncOnce(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Error, errEndpointBinding) || !strings.Contains(status.Error, "端点绑定") || strings.Contains(status.Error, "foreign.example") {
		t.Fatalf("status error = %q", status.Error)
	}
	if first.hit("root.json") == 0 || first.hit("index.json") != 0 || first.hit("revocations.json") != 0 || secondHits == 0 {
		t.Fatalf("first root=%d index=%d revocations=%d second=%d", first.hit("root.json"), first.hit("index.json"), first.hit("revocations.json"), secondHits)
	}
	after, err := repo.GetMarketState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCachedDocs(before, after) || after.RootSeq != 4 || after.LastError != status.Error || !strings.Contains(after.LastError, "endpoint_binding") {
		t.Fatalf("cache changed or last_error = %q\nbefore=%+v\nafter=%+v", after.LastError, before, after)
	}
}

func TestSyncOnceManualSourceSkipsEndpointBinding(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	docs := signDocsRegistries(t, rootPriv, online, 1, 2, 3, gen, exp, []string{"https://foreign.example/v1/"})
	cloud := newCloud(docs)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{"https://unused.example/v1/"}, conf)
	status, err := svc.SyncOnce(context.Background(), true)
	if err != nil || status.Error != "" || status.SourceURL != base || status.Root.Seq != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	state, err := repo.GetMarketState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.RootSeq != 1 || state.IndexSeq != 2 || state.RevocationsSeq != 3 || state.SourceURL != base || state.LastError != "" || !bytes.Equal(state.RootEnvelope, docs.root) {
		t.Fatalf("state = %+v", state)
	}
}

func TestSyncBaseAcceptsRootListingBothBases(t *testing.T) {
	rootPriv, online := testKeys()
	gen, exp := freshWindow()
	oldBase := "https://old.example/v1/"
	newBase := "https://new.example/v1/"
	docs := signDocsRegistries(t, rootPriv, online, 2, 3, 4, gen, exp, []string{oldBase, newBase})
	client := &http.Client{Transport: docsTransport(docs)}
	svc := &Service{rootKeys: []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}}
	blank := store.JSPluginMarketState{}
	for _, base := range []string{oldBase, "HTTPS://New.Example/v1"} {
		if !rootCoversBase(&marketproto.Root{Endpoints: marketproto.Endpoints{Registry: []string{oldBase, newBase}}}, base) {
			t.Fatalf("root should cover %s", base)
		}
		next, status, err := svc.syncBase(context.Background(), client, base, blank, config.PluginMarketConfig{}, true)
		if err != nil || status.Error != "" || next.RootSeq != 2 || next.IndexSeq != 3 || next.RevocationsSeq != 4 || !bytes.Equal(next.RootEnvelope, docs.root) {
			t.Fatalf("base %s status=%+v err=%v next=%+v", base, status, err, next)
		}
	}
	prior := store.JSPluginMarketState{RootEnvelope: []byte("keep"), IndexEnvelope: []byte("idx"), RevocationsEnvelope: []byte("rev")}
	rejected, status, err := svc.syncBase(context.Background(), client, "https://elsewhere.example/v1/", prior, config.PluginMarketConfig{}, true)
	if err == nil || !strings.Contains(err.Error(), errEndpointBinding) || strings.Contains(err.Error(), "https://") {
		t.Fatalf("err = %v", err)
	}
	if status.SourceURL != "" || rejected.RootSeq != 0 || len(rejected.RootEnvelope) != 0 || len(rejected.IndexEnvelope) != 0 || len(rejected.RevocationsEnvelope) != 0 {
		t.Fatalf("rejected state = %+v status=%+v", rejected, status)
	}
	if string(prior.RootEnvelope) != "keep" || string(prior.IndexEnvelope) != "idx" || string(prior.RevocationsEnvelope) != "rev" {
		t.Fatalf("cached input changed: %+v", prior)
	}
}

func TestSnapshotExpiredStillReturnsDocuments(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	now := time.Now().UTC().Truncate(time.Second)
	gen := now.Add(-48 * time.Hour)
	exp := now.Add(-24 * time.Hour)
	docs := signDocs(t, rootPriv, online, 3, 9, 4, gen, exp)
	rootSHA, err := payloadSHA256(docs.root)
	if err != nil {
		t.Fatal(err)
	}
	indexSHA, err := payloadSHA256(docs.index)
	if err != nil {
		t.Fatal(err)
	}
	revSHA, err := payloadSHA256(docs.revocations)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMarketState(context.Background(), store.JSPluginMarketState{
		RootSeq: 3, RootPayloadSHA256: rootSHA, RootEnvelope: docs.root,
		IndexSeq: 9, IndexPayloadSHA256: indexSHA, IndexEnvelope: docs.index,
		RevocationsSeq: 4, RevocationsPayloadSHA256: revSHA, RevocationsEnvelope: docs.revocations,
		SourceURL: "https://registry.example/v1/",
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, nil, nil)
	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.IndexExpired || !snap.RevocationsExpired || snap.Index == nil || snap.Revocations == nil || snap.Root == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Index.Seq != 9 || snap.Revocations.Seq != 4 || snap.Root.Seq != 3 {
		t.Fatalf("seqs root=%d index=%d revocations=%d", snap.Root.Seq, snap.Index.Seq, snap.Revocations.Seq)
	}
	if snap.Index.Plugins == nil || snap.Revocations.Entries == nil {
		t.Fatal("expired documents lost their lists")
	}
}

type cloudFile struct {
	body   []byte
	etag   string
	status int
}

type cloud struct {
	mu          sync.Mutex
	files       map[string]*cloudFile
	hits        map[string]int
	total       int
	headers     []http.Header
	holdFirst   bool
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	blockPath   string
	pathEntered chan struct{}
	pathRelease chan struct{}
	pathOnce    sync.Once
}

func newCloud(docs signedDocs) *cloud {
	h := &cloud{files: map[string]*cloudFile{}, hits: map[string]int{}}
	h.use(docs)
	return h
}

func (h *cloud) use(docs signedDocs) {
	h.put("root.json", docs.root)
	h.put("index.json", docs.index)
	h.put("revocations.json", docs.revocations)
}

func (h *cloud) put(name string, body []byte) {
	sum := sha256.Sum256(body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[name] = &cloudFile{body: body, etag: fmt.Sprintf(`"%x"`, sum[:8])}
}

func (h *cloud) hit(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[name]
}

func (h *cloud) totalHits() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

func (h *cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	h.mu.Lock()
	h.total++
	h.hits[name]++
	h.headers = append(h.headers, r.Header.Clone())
	file := h.files[name]
	block := h.holdFirst && h.total == 1
	blockPath := h.blockPath
	h.mu.Unlock()
	if block {
		h.enterOnce.Do(func() { close(h.entered) })
		<-h.release
	}
	if blockPath != "" && name == blockPath {
		h.pathOnce.Do(func() {
			if h.pathEntered != nil {
				close(h.pathEntered)
			}
		})
		if h.pathRelease != nil {
			<-h.pathRelease
		}
	}
	if file == nil {
		http.NotFound(w, r)
		return
	}
	if file.status != 0 && file.status != http.StatusOK {
		w.WriteHeader(file.status)
		return
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == file.etag {
		w.Header().Set("ETag", file.etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", file.etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.body)
}

type signedDocs struct {
	root, index, revocations []byte
}

func testKeys() (rootPriv, online ed25519.PrivateKey) {
	return fixedKey(0x11), fixedKey(0x33)
}

func fixedKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func freshWindow() (time.Time, time.Time) {
	gen := time.Now().UTC().Truncate(time.Second)
	return gen, gen.Add(7 * 24 * time.Hour)
}

func signDocs(t *testing.T, rootPriv, online ed25519.PrivateKey, rootSeq, indexSeq, revSeq int64, generated, expires time.Time) signedDocs {
	t.Helper()
	return signDocsRegistries(t, rootPriv, online, rootSeq, indexSeq, revSeq, generated, expires, []string{"https://127.0.0.1:1/v1/"})
}

func signDocsRegistries(t *testing.T, rootPriv, online ed25519.PrivateKey, rootSeq, indexSeq, revSeq int64, generated, expires time.Time, registries []string) signedDocs {
	t.Helper()
	pub := online.Public().(ed25519.PublicKey)
	root := marketproto.Root{
		V:        1,
		Seq:      rootSeq,
		IssuedAt: generated.UTC().Format(time.RFC3339),
		OnlineKeys: []marketproto.OnlineKey{{
			KeyID:     marketproto.KeyID(pub),
			PublicKey: base64.StdEncoding.EncodeToString(pub),
			Roles:     []string{marketproto.RoleIndex, marketproto.RoleRevocations, marketproto.RoleAttestation},
			NotBefore: "2020-01-01T00:00:00Z",
			NotAfter:  "2099-01-01T00:00:00Z",
		}},
		RetiredKeys: []marketproto.RetiredKey{},
		Endpoints:   marketproto.Endpoints{Registry: registries},
	}
	idx := marketproto.Index{
		V: 1, Seq: indexSeq,
		GeneratedAt: generated.UTC().Format(time.RFC3339),
		ExpiresAt:   expires.UTC().Format(time.RFC3339),
		Plugins:     []marketproto.Plugin{},
	}
	rev := marketproto.Revocations{
		V: 1, Seq: revSeq,
		GeneratedAt: generated.UTC().Format(time.RFC3339),
		ExpiresAt:   expires.UTC().Format(time.RFC3339),
		Entries:     []marketproto.RevocationEntry{},
	}
	return signedDocs{
		root:        signEnv(t, marketproto.PayloadTypeRoot, root, rootPriv),
		index:       signEnv(t, marketproto.PayloadTypeIndex, idx, online),
		revocations: signEnv(t, marketproto.PayloadTypeRevocations, rev, online),
	}
}

func signEnv(t *testing.T, payloadType string, payload any, priv ed25519.PrivateKey) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal(marketproto.Sign(payloadType, raw, priv))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func newTestService(t *testing.T, repo store.JSPluginRepository, keys []ed25519.PublicKey, registries []string, conf *config.ConfigFile) *Service {
	t.Helper()
	clearProxyEnv(t)
	svc, err := NewService(repo, httpclient.NewManager(conf), conf, keys, registries)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func marketConf(t *testing.T, cfg config.PluginMarketConfig) *config.ConfigFile {
	t.Helper()
	conf, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.Set(config.PluginMarketConfigPath, config.PluginMarketConfigToMap(cfg)); err != nil {
		t.Fatal(err)
	}
	return conf
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(key, "")
	}
}

func assertAnonymous(t *testing.T, headers []http.Header, owner string) {
	t.Helper()
	want := "xLyra/" + version.Current().Version
	if len(headers) == 0 {
		t.Fatal("no requests")
	}
	for _, hdr := range headers {
		if hdr.Get("User-Agent") != want {
			t.Fatalf("User-Agent = %q", hdr.Get("User-Agent"))
		}
		for key, values := range hdr {
			switch key {
			case "User-Agent", "If-None-Match", "Accept-Encoding":
			default:
				t.Fatalf("unexpected header %s", key)
			}
			for _, value := range values {
				if owner != "" && strings.Contains(value, owner) {
					t.Fatalf("header %s contains owner", key)
				}
			}
		}
	}
}

func sameCachedDocs(a, b store.JSPluginMarketState) bool {
	return a.RootSeq == b.RootSeq &&
		a.IndexSeq == b.IndexSeq &&
		a.RevocationsSeq == b.RevocationsSeq &&
		a.RootPayloadSHA256 == b.RootPayloadSHA256 &&
		a.IndexPayloadSHA256 == b.IndexPayloadSHA256 &&
		a.RevocationsPayloadSHA256 == b.RevocationsPayloadSHA256 &&
		bytes.Equal(a.RootEnvelope, b.RootEnvelope) &&
		bytes.Equal(a.IndexEnvelope, b.IndexEnvelope) &&
		bytes.Equal(a.RevocationsEnvelope, b.RevocationsEnvelope) &&
		a.RootETag == b.RootETag &&
		a.IndexETag == b.IndexETag &&
		a.RevocationsETag == b.RevocationsETag &&
		a.SourceURL == b.SourceURL &&
		timeEqual(a.IndexSyncedAt, b.IndexSyncedAt)
}

func timeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func stateFromDocs(t *testing.T, docs signedDocs, rootSeq, indexSeq, revSeq int64) store.JSPluginMarketState {
	t.Helper()
	rootSHA, err := payloadSHA256(docs.root)
	if err != nil {
		t.Fatal(err)
	}
	indexSHA, err := payloadSHA256(docs.index)
	if err != nil {
		t.Fatal(err)
	}
	revSHA, err := payloadSHA256(docs.revocations)
	if err != nil {
		t.Fatal(err)
	}
	return store.JSPluginMarketState{
		RootSeq: rootSeq, RootPayloadSHA256: rootSHA, RootEnvelope: docs.root,
		IndexSeq: indexSeq, IndexPayloadSHA256: indexSHA, IndexEnvelope: docs.index,
		RevocationsSeq: revSeq, RevocationsPayloadSHA256: revSHA, RevocationsEnvelope: docs.revocations,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func docsTransport(docs signedDocs) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case strings.HasSuffix(r.URL.Path, "/root.json"):
			body = docs.root
		case strings.HasSuffix(r.URL.Path, "/index.json"):
			body = docs.index
		case strings.HasSuffix(r.URL.Path, "/revocations.json"):
			body = docs.revocations
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})
}
