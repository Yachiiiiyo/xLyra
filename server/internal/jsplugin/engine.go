package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Yachiiiiyo/moejs"
)

var errHookTimeout = errors.New("hook timeout")

// Reusable ObjectTemplates for hot-path hook input objects. Keys are ordered
// alphabetically to match moejs.FromGo map enumeration order; optional fields
// use separate templates so omitted keys are absent rather than undefined.
var (
	tplStreamEventDataOnly   = moejs.NewObjectTemplate("data")
	tplStreamEventFull       = moejs.NewObjectTemplate("data", "event")
	tplStreamCtxBase         = moejs.NewObjectTemplate("model", "protocol", "siteType")
	tplStreamCtxWithSettings = moejs.NewObjectTemplate("model", "protocol", "settings", "siteType")

	tplSegmentItemBase     = moejs.NewObjectTemplate("field", "id", "text")
	tplSegmentItemWithRole = moejs.NewObjectTemplate("field", "id", "role", "text")

	tplFindingBase   = moejs.NewObjectTemplate("end", "segment", "start", "type")
	tplFindingSource = moejs.NewObjectTemplate("end", "segment", "source", "start", "type")
	tplFindingScore  = moejs.NewObjectTemplate("end", "score", "segment", "start", "type")
	tplFindingFull   = moejs.NewObjectTemplate("end", "score", "segment", "source", "start", "type")

	tplInspectBinding         = moejs.NewObjectTemplate("id", "params")
	tplInspectCtxBase         = moejs.NewObjectTemplate("binding", "endpoint", "model")
	tplInspectCtxWithSettings = moejs.NewObjectTemplate("binding", "endpoint", "model", "settings")
	tplInspectInput           = moejs.NewObjectTemplate("findings", "segments")
)

// RuntimeStats is a pure-Go snapshot of a pooled JS runtime's resource usage
// and cumulative counters.
type RuntimeStats struct {
	AllocatedBytes     int64
	LiveBytes          int64
	AllocLimitHits     int64
	Interrupts         int64
	ICEntries          int
	PendingJobs        int
	RegisterStackBytes int64
}

// program is one compiled plugin module. It is safe to load from many runtimes.
type program struct {
	mod   *moejs.Module
	hooks map[string]moejs.Hook
}

// addOptionalHooks resolves the hooks a kind allows but does not require. A
// hook the module does not export is simply absent from p.hooks.
func (p *program) addOptionalHooks(names []string) error {
	for _, name := range names {
		hook, err := p.mod.Hook(name)
		if errors.Is(err, moejs.ErrHookNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s hook: %w", name, err)
		}
		p.hooks[name] = hook
	}
	return nil
}

// requireCallableHooks rejects an optional export that exists and is not a
// function. A missing export is allowed.
func (s *session) requireCallableHooks(names []string) error {
	for _, name := range names {
		value, ok := s.rt.Export(name)
		if !ok {
			continue
		}
		if !value.IsObject() || !value.AsObject().IsCallable() {
			return fmt.Errorf("%s hook: %w", name, moejs.ErrNotCallable)
		}
	}
	s.rt.ReleaseCallData()
	return nil
}

// requireHooks registers exports that must exist. A missing export fails.
// Whether the value is a function is checked by requireExports.
func (p *program) requireHooks(names []string) error {
	for _, name := range names {
		hook, err := p.mod.Hook(name)
		if errors.Is(err, moejs.ErrHookNotFound) {
			return fmt.Errorf("export %q is required", name)
		}
		if err != nil {
			return fmt.Errorf("%s hook: %w", name, err)
		}
		p.hooks[name] = hook
	}
	return nil
}

// requireExports rejects a missing export or one that is not a function.
func (s *session) requireExports(names []string) error {
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		value, ok := s.rt.Export(name)
		if !ok {
			return fmt.Errorf("export %q is required", name)
		}
		if !value.IsObject() || !value.AsObject().IsCallable() {
			return fmt.Errorf("%s hook: %w", name, moejs.ErrNotCallable)
		}
	}
	s.rt.ReleaseCallData()
	return nil
}

func compileProgram(name, source string, hookNames ...string) (*program, error) {
	mod, err := moejs.Compile(name, source)
	if err != nil {
		return nil, err
	}
	if requests := mod.Requests(); len(requests) > 0 {
		return nil, fmt.Errorf("module imports %q", requests[0])
	}
	hooks := make(map[string]moejs.Hook, len(hookNames))
	for _, hookName := range hookNames {
		hook, err := mod.Hook(hookName)
		if err != nil {
			return nil, fmt.Errorf("%s hook: %w", hookName, err)
		}
		hooks[hookName] = hook
	}
	return &program{mod: mod, hooks: hooks}, nil
}

type callLog struct {
	mu      sync.Mutex
	entries []LogEntry
	dropped int
}

func (l *callLog) reset() {
	l.mu.Lock()
	l.entries = nil
	l.dropped = 0
	l.mu.Unlock()
}

// write keeps message plus encoded fields within maxPluginLogBytes. When the
// pair is too long, the fields are folded into the clipped message text.
func (l *callLog) write(level, message string, fields map[string]any) {
	if len(fields) > 0 {
		payload, err := json.Marshal(fields)
		if err != nil {
			payload = []byte(`{"error":"fields are not JSON"}`)
			fields = nil
		}
		if len(message)+1+len(payload) > maxPluginLogBytes {
			message = clipUTF8Bytes(message+" "+string(payload), maxPluginLogBytes)
			fields = nil
		}
	} else {
		message = clipUTF8Bytes(message, maxPluginLogBytes)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= maxPluginLogs {
		l.dropped++
		return
	}
	l.entries = append(l.entries, LogEntry{Level: level, Message: message, Fields: fields})
}

func (l *callLog) snapshot() ([]LogEntry, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]LogEntry(nil), l.entries...)
	return out, l.dropped
}

// clipUTF8Bytes returns at most limit bytes, ending in U+FFFD when cut.
func clipUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := value[:limit-len("\uFFFD")]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\uFFFD"
}

// callEnv carries per-call capabilities attached to a borrowed session.
type callEnv struct {
	ctx           context.Context
	allowsStorage bool
	storage       StorageBackend
}

// session is one runtime borrowed from a pool.
type session struct {
	rt       *moejs.Runtime
	logs     *callLog
	env      callEnv
	liveDebt int64
}

func (s *session) checkLiveLimit(allocDelta int64) error {
	if MaxLiveBytes <= 0 {
		return nil
	}
	if allocDelta > 0 {
		s.liveDebt += allocDelta
	}
	if s.rt.Stats().LiveBytes+s.liveDebt <= MaxLiveBytes {
		return nil
	}
	s.liveDebt = 0
	return s.rt.CheckLiveLimit()
}

func (s *session) stats() RuntimeStats {
	st := s.rt.Stats()
	return RuntimeStats{
		AllocatedBytes:     st.AllocatedBytes,
		LiveBytes:          st.LiveBytes,
		AllocLimitHits:     st.AllocLimitHits,
		Interrupts:         st.Interrupts,
		ICEntries:          st.ICEntries,
		PendingJobs:        st.PendingJobs,
		RegisterStackBytes: st.RegisterStackBytes,
	}
}

func newSession(p *program) (*session, error) {
	logs := &callLog{}
	rt := moejs.NewRuntime(moejs.Options{
		DisableDynamicCode: true,
		MutableIntrinsics:  false,
		TimeZone:           time.UTC,
		MaxAllocBytes:      MaxAllocBytes,
		MaxResultBytes:     MaxResultBytes,
		MaxResultNodes:     MaxResultNodes,
		MaxLiveBytes:       MaxLiveBytes,
	})
	sess := &session{rt: rt, logs: logs}
	if err := installHost(sess); err != nil {
		return nil, err
	}
	if err := rt.Load(p.mod); err != nil {
		return nil, err
	}
	if err := rt.CheckLiveLimit(); err != nil {
		return nil, err
	}
	return sess, nil
}

func installHost(sess *session) error {
	rt := sess.rt
	logs := sess.logs
	namespaces := []struct {
		name  string
		value map[string]any
	}{
		{
			name: "bytes",
			value: map[string]any{
				"fromUTF8":      hostBytesFromUTF8(),
				"toUTF8":        hostStrictBytesToString(bytesToUTF8),
				"fromHex":       hostStringToBytes(bytesFromHex),
				"toHex":         hostStrictBytesToString(bytesToHex),
				"fromBase64":    hostStringToBytes(bytesFromBase64),
				"toBase64":      hostStrictBytesToString(bytesToBase64),
				"fromBase64URL": hostStringToBytes(bytesFromBase64URL),
				"toBase64URL":   hostStrictBytesToString(bytesToBase64URL),
				"concat":        hostBytesConcat(),
				"equal":         hostBytesEqual(),
			},
		},
		{
			name: "encoding",
			value: map[string]any{
				"hexEncode":       hostBinaryToString(bytesToHex),
				"hexDecode":       hostStringToBytes(bytesFromHex),
				"base64Encode":    hostBinaryToString(bytesToBase64),
				"base64Decode":    hostStringToBytes(bytesFromBase64),
				"base64URLEncode": hostBinaryToString(bytesToBase64URL),
				"base64URLDecode": hostStringToBytes(bytesFromBase64URL),
				"utf8Encode":      hostBytesFromUTF8(),
				"utf8Decode":      hostStrictBytesToString(bytesToUTF8),
			},
		},
		{
			name: "crypto",
			value: map[string]any{
				"hmacSHA256":  hostString2(hmacSHA256),
				"sha256":      hostDigestFixed("sha256"),
				"md5":         hostDigestFixed("md5"),
				"sha1":        hostDigestFixed("sha1"),
				"sha512":      hostDigestFixed("sha512"),
				"digestBytes": hostDigestBytes(),
				"hmac":        hostHMAC(),
				"hmacBytes":   hostHMACBytes(),
				"randomBytes": hostRandomBytes(),
				"aesEncrypt":  hostAES(aesEncrypt),
				"aesDecrypt":  hostAES(aesDecrypt),
				"rsaEncrypt":  hostRSAEncrypt(),
			},
		},
		{
			name: "codec",
			value: map[string]any{
				"base64Encode":    hostString1(base64Encode),
				"base64Decode":    hostString1(base64Decode),
				"base64URLEncode": hostString1(base64URLEncode),
				"base64URLDecode": hostString1(base64URLDecode),
			},
		},
		{
			name: "jwt",
			value: map[string]any{
				"signHS256":   hostPayload(signHS256),
				"decodeHS256": hostString2(decodeHS256),
			},
		},
		{
			name: "utils",
			value: map[string]any{
				"uuid": hostString0(newUUID),
			},
		},
		{
			name: "storage",
			value: map[string]any{
				"get":    hostStorageGet(sess),
				"set":    hostStorageSet(sess),
				"delete": hostStorageDelete(sess),
				"keys":   hostStorageKeys(sess),
			},
		},
		{
			name: "log",
			value: map[string]any{
				"debug": hostLog(logs, "debug"),
				"info":  hostLog(logs, "info"),
				"warn":  hostLog(logs, "warn"),
			},
		},
	}
	for _, ns := range namespaces {
		if err := rt.SetGlobal(ns.name, ns.value); err != nil {
			return err
		}
	}
	rt.ReleaseCallData()
	return nil
}

func hostString0(fn func() (string, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		out, err := fn()
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostString1(fn func(string) (string, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		text, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(text)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostString2[T any](fn func(string, string) (T, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		first, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		second, err := argString(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(first, second)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostBytesFromUTF8() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		text, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		if len(text) > maxBinaryInputBytes {
			return moejs.Undefined(), fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes([]byte(text)))), nil
	}
}

func hostStringToBytes(fn func(string) ([]byte, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		text, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(text)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostStrictBytesToString(fn func([]byte) (string, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		data, err := argStrictBytes(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(data)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostBinaryToString(fn func([]byte) (string, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		data, err := argBinary(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(data)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostBytesConcat() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		chunks, err := argBytesSlice(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := bytesConcat(chunks)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostBytesEqual() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		a, err := argStrictBytes(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		b, err := argStrictBytes(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(bytesEqualConstantTime(a, b))
	}
}

func hostDigestFixed(alg string) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		data, err := argBinary(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		outEnc, err := argOptionalString(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := digestEncoded(alg, data, outEnc)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostDigestBytes() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		alg, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		data, err := argBinary(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := digestBytes(alg, data)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostHMAC() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		alg, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argBinary(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		data, err := argBinary(r, args, 2)
		if err != nil {
			return moejs.Undefined(), err
		}
		outEnc, err := argOptionalString(r, args, 3)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := hmacDigestEncoded(alg, key, data, outEnc)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostHMACBytes() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		alg, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argBinary(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		data, err := argBinary(r, args, 2)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := hmacDigestBytes(alg, key, data)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostRandomBytes() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		length, err := argInt(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := cryptoRandomBytes(length)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostAES(fn func(string, []byte, []byte, []byte, []byte) ([]byte, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		alg, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argBinary(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		iv, err := argBinary(r, args, 2)
		if err != nil {
			return moejs.Undefined(), err
		}
		data, err := argBinary(r, args, 3)
		if err != nil {
			return moejs.Undefined(), err
		}
		aad, err := argOptionalBinary(r, args, 4)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(alg, key, iv, data, aad)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostRSAEncrypt() moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		pemKey, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		plain, err := argBinary(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		padding, err := argOptionalString(r, args, 2)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := rsaEncryptPEM(pemKey, plain, padding)
		if err != nil {
			return moejs.Undefined(), err
		}
		return moejs.ObjectValue(r.NewUint8Array(nonNilBytes(out))), nil
	}
}

func hostStorageReady(sess *session) (context.Context, StorageBackend, error) {
	if !sess.env.allowsStorage {
		return nil, nil, permissionDeniedError("storage permission is not declared")
	}
	if sess.env.storage == nil {
		return nil, nil, permissionDeniedError("storage is not available in this context")
	}
	ctx := sess.env.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, sess.env.storage, nil
}

func hostStorageGet(sess *session) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		ctx, store, err := hostStorageReady(sess)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		val, err := store.Get(ctx, key)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(val)
	}
}

func hostStorageSet(sess *session) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		ctx, store, err := hostStorageReady(sess)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		rawVal := moejs.Arg(args, 1)
		if rawVal == moejs.Undefined() {
			return moejs.Undefined(), errors.New("storage value must not be undefined")
		}
		value, err := r.ToGoStrict(rawVal)
		if err != nil {
			return moejs.Undefined(), err
		}
		if err := store.Set(ctx, key, value); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	}
}

func hostStorageDelete(sess *session) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		ctx, store, err := hostStorageReady(sess)
		if err != nil {
			return moejs.Undefined(), err
		}
		key, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		deleted, err := store.Delete(ctx, key)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(deleted)
	}
}

func hostStorageKeys(sess *session) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		ctx, store, err := hostStorageReady(sess)
		if err != nil {
			return moejs.Undefined(), err
		}
		keys, err := store.Keys(ctx)
		if err != nil {
			return moejs.Undefined(), err
		}
		if keys == nil {
			keys = []string{}
		}
		return r.FromGo(keys)
	}
}

func hostPayload(fn func(any, string) (string, error)) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		payload, err := r.ToGoStrict(moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), err
		}
		secret, err := argString(r, args, 1)
		if err != nil {
			return moejs.Undefined(), err
		}
		out, err := fn(payload, secret)
		if err != nil {
			return moejs.Undefined(), err
		}
		return r.FromGo(out)
	}
}

func hostLog(logs *callLog, level string) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		message, err := argString(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		var fields map[string]any
		if len(args) > 1 {
			value, err := r.ToGoStrict(moejs.Arg(args, 1))
			if err != nil {
				return moejs.Undefined(), err
			}
			if object, ok := value.(map[string]any); ok {
				fields = object
			}
		}
		logs.write(level, message, fields)
		return moejs.Undefined(), nil
	}
}

func argString(r *moejs.Realm, args []moejs.Value, index int) (string, error) {
	value, err := r.ToGoStrict(moejs.Arg(args, index))
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("expected string")
	}
	return text, nil
}

func argOptionalString(r *moejs.Realm, args []moejs.Value, index int) (string, error) {
	if index >= len(args) {
		return "", nil
	}
	raw := moejs.Arg(args, index)
	if raw == moejs.Undefined() {
		return "", nil
	}
	value, err := r.ToGoStrict(raw)
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("expected string")
	}
	return text, nil
}

func argInt(r *moejs.Realm, args []moejs.Value, index int) (int, error) {
	value, err := r.ToGoStrict(moejs.Arg(args, index))
	if err != nil {
		return 0, err
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return 0, errors.New("expected integer")
		}
		return int(typed), nil
	default:
		return 0, errors.New("expected integer")
	}
}

func isDetachedByteBuffer(v moejs.Value) bool {
	if !v.IsObject() {
		return false
	}
	o := v.AsObject()
	switch o.ClassName() {
	case "TypedArray", "ArrayBuffer", "SharedArrayBuffer", "DataView":
		_, ok := o.BytesView()
		return !ok
	default:
		return false
	}
}

func decodeBinaryValue(raw moejs.Value) ([]byte, error) {
	if raw.IsString() {
		text := raw.AsString().GoString()
		if len(text) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return []byte(text), nil
	}
	if data, ok := raw.Uint8ArrayBytes(); ok {
		if len(data) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return data, nil
	}
	if isDetachedByteBuffer(raw) {
		return nil, errors.New("detached byte buffer")
	}
	return nil, errors.New("expected string or Uint8Array")
}

func argBinary(_ *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	return decodeBinaryValue(moejs.Arg(args, index))
}

func argOptionalBinary(_ *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	if index >= len(args) {
		return nil, nil
	}
	raw := moejs.Arg(args, index)
	if raw.IsUndefined() || raw.IsNull() {
		return nil, nil
	}
	return decodeBinaryValue(raw)
}

func argStrictBytes(_ *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	raw := moejs.Arg(args, index)
	if data, ok := raw.Uint8ArrayBytes(); ok {
		if len(data) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return data, nil
	}
	if raw.IsObject() && raw.AsObject().ClassName() == "TypedArray" {
		if _, ok := raw.BytesView(); !ok {
			return nil, errors.New("detached byte buffer")
		}
	}
	return nil, errors.New("expected Uint8Array")
}

func decodeStrictBytesChunk(elem moejs.Value, i int) ([]byte, error) {
	if chunk, ok := elem.Uint8ArrayBytes(); ok {
		if len(chunk) > maxBinaryInputBytes {
			return nil, fmt.Errorf("chunks[%d]: input exceeds %d bytes", i, maxBinaryInputBytes)
		}
		return chunk, nil
	}
	if elem.IsObject() && elem.AsObject().ClassName() == "TypedArray" {
		if _, ok := elem.BytesView(); !ok {
			return nil, fmt.Errorf("chunks[%d]: detached byte buffer", i)
		}
	}
	return nil, fmt.Errorf("chunks[%d]: expected Uint8Array", i)
}

func argBytesSlice(r *moejs.Realm, args []moejs.Value, index int) ([][]byte, error) {
	raw := moejs.Arg(args, index)
	if !raw.IsObject() || raw.AsObject().ClassName() != "Array" {
		return nil, errors.New("expected array of Uint8Array")
	}
	o := raw.AsObject()
	if o.IsDenseArray() {
		elems := o.Elements()
		out := make([][]byte, len(elems))
		for i, elem := range elems {
			chunk, err := decodeStrictBytesChunk(elem, i)
			if err != nil {
				return nil, err
			}
			out[i] = chunk
		}
		return out, nil
	}
	n := int(o.ArrayLength())
	out := make([][]byte, 0, min(n, 64))
	for i := 0; i < n; i++ {
		if i&1023 == 0 {
			if err := r.CheckInterrupt(); err != nil {
				return nil, err
			}
		}
		elem, err := o.GetIndex(r, uint32(i))
		if err != nil {
			return nil, err
		}
		chunk, err := decodeStrictBytesChunk(elem, i)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, nil
}

func argToValue(rt *moejs.Runtime, arg any) (moejs.Value, error) {
	switch v := arg.(type) {
	case StreamTransformEvent:
		return streamEventToValue(rt, v), nil
	case *StreamTransformEvent:
		if v == nil {
			return moejs.Null(), nil
		}
		return streamEventToValue(rt, *v), nil
	case StreamTransformContext:
		return streamCtxToValue(rt, v)
	case *StreamTransformContext:
		if v == nil {
			return moejs.Null(), nil
		}
		return streamCtxToValue(rt, *v)
	case SegmentItem:
		return segmentItemToValue(rt, v), nil
	case *SegmentItem:
		if v == nil {
			return moejs.Null(), nil
		}
		return segmentItemToValue(rt, *v), nil
	case SegmentFinding:
		return segmentFindingToValue(rt, v), nil
	case *SegmentFinding:
		if v == nil {
			return moejs.Null(), nil
		}
		return segmentFindingToValue(rt, *v), nil
	case []SegmentItem:
		return segmentItemsToValue(rt, v)
	case []SegmentFinding:
		return segmentFindingsToValue(rt, v)
	case InspectSegmentsBinding:
		return inspectBindingToValue(rt, v)
	case InspectSegmentsContext:
		return inspectCtxToValue(rt, v)
	case InspectSegmentsInput:
		return inspectInputToValue(rt, v)
	default:
		return rt.FromGo(arg)
	}
}

func streamEventToValue(rt *moejs.Runtime, e StreamTransformEvent) moejs.Value {
	if e.Event == "" {
		return rt.NewObjectFrom(tplStreamEventDataOnly, moejs.String(e.Data))
	}
	return rt.NewObjectFrom(tplStreamEventFull, moejs.String(e.Data), moejs.String(e.Event))
}

func streamCtxToValue(rt *moejs.Runtime, c StreamTransformContext) (moejs.Value, error) {
	if len(c.Settings) == 0 {
		return rt.NewObjectFrom(tplStreamCtxBase, moejs.String(c.Model), moejs.String(c.Protocol), moejs.String(c.SiteType)), nil
	}
	settingsVal, err := rt.FromGo(c.Settings)
	if err != nil {
		return moejs.Undefined(), err
	}
	return rt.NewObjectFrom(tplStreamCtxWithSettings, moejs.String(c.Model), moejs.String(c.Protocol), settingsVal, moejs.String(c.SiteType)), nil
}

func segmentItemToValue(rt *moejs.Runtime, s SegmentItem) moejs.Value {
	if s.Role == "" {
		return rt.NewObjectFrom(tplSegmentItemBase, moejs.String(s.Field), moejs.String(s.ID), moejs.String(s.Text))
	}
	return rt.NewObjectFrom(tplSegmentItemWithRole, moejs.String(s.Field), moejs.String(s.ID), moejs.String(s.Role), moejs.String(s.Text))
}

func segmentFindingToValue(rt *moejs.Runtime, f SegmentFinding) moejs.Value {
	hasSource := f.Source != ""
	hasScore := f.Score != 0
	switch {
	case hasSource && hasScore:
		return rt.NewObjectFrom(tplFindingFull, moejs.Int(int64(f.End)), moejs.Number(f.Score), moejs.String(f.Segment), moejs.String(f.Source), moejs.Int(int64(f.Start)), moejs.String(f.Type))
	case hasSource:
		return rt.NewObjectFrom(tplFindingSource, moejs.Int(int64(f.End)), moejs.String(f.Segment), moejs.String(f.Source), moejs.Int(int64(f.Start)), moejs.String(f.Type))
	case hasScore:
		return rt.NewObjectFrom(tplFindingScore, moejs.Int(int64(f.End)), moejs.Number(f.Score), moejs.String(f.Segment), moejs.Int(int64(f.Start)), moejs.String(f.Type))
	default:
		return rt.NewObjectFrom(tplFindingBase, moejs.Int(int64(f.End)), moejs.String(f.Segment), moejs.Int(int64(f.Start)), moejs.String(f.Type))
	}
}

func segmentItemsToValue(rt *moejs.Runtime, items []SegmentItem) (moejs.Value, error) {
	if len(items) == 0 {
		return rt.FromGo([]any{})
	}
	vals := make([]moejs.Value, len(items))
	for i, item := range items {
		vals[i] = segmentItemToValue(rt, item)
	}
	return moejs.ObjectValue(rt.Realm().NewArrayFromSlice(vals)), nil
}

func segmentFindingsToValue(rt *moejs.Runtime, items []SegmentFinding) (moejs.Value, error) {
	if len(items) == 0 {
		return rt.FromGo([]any{})
	}
	vals := make([]moejs.Value, len(items))
	for i, item := range items {
		vals[i] = segmentFindingToValue(rt, item)
	}
	return moejs.ObjectValue(rt.Realm().NewArrayFromSlice(vals)), nil
}

func inspectBindingToValue(rt *moejs.Runtime, b InspectSegmentsBinding) (moejs.Value, error) {
	paramsVal, err := rt.FromGo(b.Params)
	if err != nil {
		return moejs.Undefined(), err
	}
	return rt.NewObjectFrom(tplInspectBinding, moejs.String(b.ID), paramsVal), nil
}

func inspectCtxToValue(rt *moejs.Runtime, c InspectSegmentsContext) (moejs.Value, error) {
	bindingVal, err := inspectBindingToValue(rt, c.Binding)
	if err != nil {
		return moejs.Undefined(), err
	}
	if c.Settings == nil {
		return rt.NewObjectFrom(tplInspectCtxBase, bindingVal, moejs.String(c.Endpoint), moejs.String(c.Model)), nil
	}
	settingsVal, err := rt.FromGo(c.Settings)
	if err != nil {
		return moejs.Undefined(), err
	}
	return rt.NewObjectFrom(tplInspectCtxWithSettings, bindingVal, moejs.String(c.Endpoint), moejs.String(c.Model), settingsVal), nil
}

func inspectInputToValue(rt *moejs.Runtime, in InspectSegmentsInput) (moejs.Value, error) {
	findingsVal, err := segmentFindingsToValue(rt, in.Findings)
	if err != nil {
		return moejs.Undefined(), err
	}
	segmentsVal, err := segmentItemsToValue(rt, in.Segments)
	if err != nil {
		return moejs.Undefined(), err
	}
	return rt.NewObjectFrom(tplInspectInput, findingsVal, segmentsVal), nil
}

func allocLimitCallError(err error, logs []LogEntry, dropped int) *CallError {
	detail := err.Error()
	var lim *moejs.AllocLimitError
	if errors.As(err, &lim) && lim.Stack != "" {
		stack := lim.Stack
		if len(stack) > 4096 {
			stack = stack[:4096]
		}
		detail += "\n" + stack
	}
	return &CallError{
		Kind:        KindHeapLimit,
		Interrupt:   "alloc_limit",
		Detail:      detail,
		Err:         err,
		Logs:        logs,
		DroppedLogs: dropped,
	}
}

func (s *session) call(ctx context.Context, timeout time.Duration, hook moejs.Hook, args []any) (any, []LogEntry, int, bool, error) {
	return s.callWithEnv(ctx, timeout, hook, callEnv{}, args)
}

func (s *session) callWithEnv(ctx context.Context, timeout time.Duration, hook moejs.Hook, env callEnv, args []any) (any, []LogEntry, int, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, false, err
	}
	s.logs.reset()
	env.ctx = ctx
	s.env = env
	defer func() { s.env = callEnv{} }()

	values := make([]moejs.Value, len(args))
	var callAlloc int64
	for i, arg := range args {
		value, err := argToValue(s.rt, arg)
		callAlloc += s.rt.AllocatedBytes()
		if err != nil {
			s.rt.ReleaseCallData()
			if errors.Is(err, moejs.ErrAllocLimit) {
				return nil, nil, 0, true, allocLimitCallError(err, nil, 0)
			}
			return nil, nil, 0, false, err
		}
		values[i] = value
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, timeout, errHookTimeout)
	defer cancel()

	var result moejs.Value
	err := s.rt.RunContext(callCtx, func(rt *moejs.Runtime) error {
		var callErr error
		result, callErr = rt.Call(hook, values...)
		return callErr
	})
	callAlloc += s.rt.AllocatedBytes()
	logs, dropped := s.logs.snapshot()
	if ctx.Err() != nil {
		return nil, logs, dropped, true, ctx.Err()
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, allocLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindResponseTooLarge, Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		if errors.Is(err, errHookTimeout) || errors.Is(context.Cause(callCtx), errHookTimeout) {
			reason = "timeout"
		}
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ClearInterrupt()
	if err != nil {
		wrapped := wrapEngineError(err)
		if call, ok := wrapped.(*CallError); ok {
			call.Logs = logs
			call.DroppedLogs = dropped
		}
		if fatalRuntime(wrapped) {
			return nil, logs, dropped, true, wrapped
		}
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, wrapped
	}
	if _, _, ok := moejs.PromiseResult(result); ok {
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindShape, Detail: "hook returned a promise", Logs: logs, DroppedLogs: dropped}
	}
	out, err := s.rt.ToGo(result)
	callAlloc += s.rt.AllocatedBytes()
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindResponseTooLarge, Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, allocLimitCallError(err, logs, dropped)
	}
	s.rt.ReleaseCallData()
	if err != nil {
		wrapped := wrapEngineError(err)
		if call, ok := wrapped.(*CallError); ok {
			call.Logs = logs
			call.DroppedLogs = dropped
		}
		if fatalRuntime(wrapped) {
			return nil, logs, dropped, true, wrapped
		}
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, wrapped
	}
	if liveErr := s.checkLiveLimit(callAlloc); liveErr != nil {
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "live_limit", Detail: liveErr.Error(), Err: liveErr, Logs: logs, DroppedLogs: dropped}
	}
	return out, logs, dropped, false, nil
}

func (s *session) exportMeta() (map[string]any, error) {
	value, ok := s.rt.Export("meta")
	if !ok {
		return nil, fmt.Errorf("meta export is missing")
	}
	out, err := s.rt.ToGo(value)
	s.rt.ReleaseCallData()
	if err != nil {
		return nil, err
	}
	meta, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("meta export is not an object")
	}
	return meta, nil
}

func interruptReason(err error) (string, bool) {
	var interrupted *moejs.InterruptedError
	if !errors.As(err, &interrupted) {
		return "", false
	}
	if interrupted.Value == nil {
		return "interrupted", true
	}
	return fmt.Sprint(interrupted.Value), true
}

func fatalRuntime(err error) bool {
	if err == nil {
		return false
	}
	var call *CallError
	if errors.As(err, &call) && (call.Kind == KindTimeout || call.Kind == KindHeapLimit) {
		return true
	}
	var internal *moejs.InternalError
	return errors.As(err, &internal)
}

func wrapEngineError(err error) error {
	var call *CallError
	if errors.As(err, &call) {
		return call
	}
	var exception *moejs.Exception
	if errors.As(err, &exception) {
		detail := exception.Message()
		if detail == "" {
			detail = exception.Error()
		}
		stack := exception.Stack
		if len(stack) > 4096 {
			stack = stack[:4096]
		}
		if stack != "" {
			detail += "\n" + stack
		}
		return &CallError{Kind: KindException, Detail: detail, Err: err}
	}
	var internal *moejs.InternalError
	if errors.As(err, &internal) {
		return &CallError{Kind: KindInternal, Detail: internal.Error(), Err: err}
	}
	return &CallError{Kind: KindException, Detail: err.Error(), Err: err}
}
