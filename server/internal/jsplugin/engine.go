package jsplugin

import (
	"bytes"
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

var hostBootstrapScript = mustCompileHostBootstrap()

func mustCompileHostBootstrap() *moejs.Script {
	const src = `(() => {
  const raw = globalThis.__xlyraRawHost;
  delete globalThis.__xlyraRawHost;
  const wrap = (fn) => (...args) => new Uint8Array(fn(...args));
  globalThis.bytes = {
    fromUTF8: wrap(raw.bytesFromUTF8),
    toUTF8: raw.bytesToUTF8,
    fromHex: wrap(raw.bytesFromHex),
    toHex: raw.bytesToHex,
    fromBase64: wrap(raw.bytesFromBase64),
    toBase64: raw.bytesToBase64,
    fromBase64URL: wrap(raw.bytesFromBase64URL),
    toBase64URL: raw.bytesToBase64URL,
    concat: wrap(raw.bytesConcat),
    equal: raw.bytesEqual,
  };
  globalThis.encoding = {
    hexEncode: raw.encHexEncode,
    hexDecode: wrap(raw.encHexDecode),
    base64Encode: raw.encBase64Encode,
    base64Decode: wrap(raw.encBase64Decode),
    base64URLEncode: raw.encBase64URLEncode,
    base64URLDecode: wrap(raw.encBase64URLDecode),
    utf8Encode: wrap(raw.bytesFromUTF8),
    utf8Decode: raw.bytesToUTF8,
  };
  globalThis.crypto = {
    hmacSHA256: raw.hmacSHA256,
    sha256: raw.sha256,
    md5: raw.md5,
    sha1: raw.sha1,
    sha512: raw.sha512,
    digestBytes: wrap(raw.digestBytes),
    hmac: raw.hmac,
    hmacBytes: wrap(raw.hmacBytes),
    randomBytes: wrap(raw.randomBytes),
    aesEncrypt: wrap(raw.aesEncrypt),
    aesDecrypt: wrap(raw.aesDecrypt),
    rsaEncrypt: wrap(raw.rsaEncrypt),
  };
})();`
	script, err := moejs.CompileScript("__xlyra_host_bootstrap.js", src)
	if err != nil {
		panic("compile host bootstrap: " + err.Error())
	}
	return script
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
	rt   *moejs.Runtime
	logs *callLog
	env  callEnv
}

func newSession(p *program) (*session, error) {
	logs := &callLog{}
	rt := moejs.NewRuntime(moejs.Options{
		DisableDynamicCode: true,
		MutableIntrinsics:  false,
		TimeZone:           time.UTC,
		MaxAllocBytes:      MaxAllocBytes,
		MaxResultBytes:     MaxResultBytes,
	})
	sess := &session{rt: rt, logs: logs}
	if err := installHost(sess); err != nil {
		return nil, err
	}
	if err := rt.Load(p.mod); err != nil {
		return nil, err
	}
	return sess, nil
}

func installHost(sess *session) error {
	rt := sess.rt
	logs := sess.logs
	rawHost := map[string]any{
		"bytesFromUTF8":      hostBytesFromUTF8(),
		"bytesToUTF8":        hostStrictBytesToString(bytesToUTF8),
		"bytesFromHex":       hostStringToBytes(bytesFromHex),
		"bytesToHex":         hostStrictBytesToString(bytesToHex),
		"bytesFromBase64":    hostStringToBytes(bytesFromBase64),
		"bytesToBase64":      hostStrictBytesToString(bytesToBase64),
		"bytesFromBase64URL": hostStringToBytes(bytesFromBase64URL),
		"bytesToBase64URL":   hostStrictBytesToString(bytesToBase64URL),
		"bytesConcat":        hostBytesConcat(),
		"bytesEqual":         hostBytesEqual(),
		"encHexEncode":       hostBinaryToString(bytesToHex),
		"encHexDecode":       hostStringToBytes(bytesFromHex),
		"encBase64Encode":    hostBinaryToString(bytesToBase64),
		"encBase64Decode":    hostStringToBytes(bytesFromBase64),
		"encBase64URLEncode": hostBinaryToString(bytesToBase64URL),
		"encBase64URLDecode": hostStringToBytes(bytesFromBase64URL),
		"hmacSHA256":         hostString2(hmacSHA256),
		"sha256":             hostDigestFixed("sha256"),
		"md5":                hostDigestFixed("md5"),
		"sha1":               hostDigestFixed("sha1"),
		"sha512":             hostDigestFixed("sha512"),
		"digestBytes":        hostDigestBytes(),
		"hmac":               hostHMAC(),
		"hmacBytes":          hostHMACBytes(),
		"randomBytes":        hostRandomBytes(),
		"aesEncrypt":         hostAES(aesEncrypt),
		"aesDecrypt":         hostAES(aesDecrypt),
		"rsaEncrypt":         hostRSAEncrypt(),
	}
	namespaces := map[string]any{
		"__xlyraRawHost": rawHost,
		"codec": map[string]any{
			"base64Encode":    hostString1(base64Encode),
			"base64Decode":    hostString1(base64Decode),
			"base64URLEncode": hostString1(base64URLEncode),
			"base64URLDecode": hostString1(base64URLDecode),
		},
		"jwt": map[string]any{
			"signHS256":   hostPayload(signHS256),
			"decodeHS256": hostString2(decodeHS256),
		},
		"utils": map[string]any{
			"uuid": hostString0(newUUID),
		},
		"storage": map[string]any{
			"get":    hostStorageGet(sess),
			"set":    hostStorageSet(sess),
			"delete": hostStorageDelete(sess),
			"keys":   hostStorageKeys(sess),
		},
		"log": map[string]any{
			"debug": hostLog(logs, "debug"),
			"info":  hostLog(logs, "info"),
			"warn":  hostLog(logs, "warn"),
		},
	}
	for name, value := range namespaces {
		if err := rt.SetGlobal(name, value); err != nil {
			return err
		}
	}
	if _, err := rt.RunScript(hostBootstrapScript); err != nil {
		return err
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
		return r.FromGo(nonNilBytes([]byte(text)))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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
		return r.FromGo(nonNilBytes(out))
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

func argBinary(r *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	value, err := r.ToGoStrict(moejs.Arg(args, index))
	if err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case string:
		if len(typed) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return []byte(typed), nil
	case []byte:
		if typed == nil {
			return nil, errors.New("detached byte buffer")
		}
		if len(typed) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return bytes.Clone(typed), nil
	default:
		return nil, errors.New("expected string or Uint8Array")
	}
}

func argOptionalBinary(r *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	if index >= len(args) {
		return nil, nil
	}
	raw := moejs.Arg(args, index)
	if raw == moejs.Undefined() {
		return nil, nil
	}
	value, err := r.ToGoStrict(raw)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case string:
		if len(typed) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return []byte(typed), nil
	case []byte:
		if typed == nil {
			return nil, errors.New("detached byte buffer")
		}
		if len(typed) > maxBinaryInputBytes {
			return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
		}
		return bytes.Clone(typed), nil
	default:
		return nil, errors.New("expected string or Uint8Array")
	}
}

func argStrictBytes(r *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	value, err := r.ToGoStrict(moejs.Arg(args, index))
	if err != nil {
		return nil, err
	}
	data, ok := value.([]byte)
	if !ok || data == nil {
		return nil, errors.New("expected Uint8Array")
	}
	if len(data) > maxBinaryInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxBinaryInputBytes)
	}
	return bytes.Clone(data), nil
}

func argBytesSlice(r *moejs.Realm, args []moejs.Value, index int) ([][]byte, error) {
	value, err := r.ToGoStrict(moejs.Arg(args, index))
	if err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("expected array of Uint8Array")
	}
	out := make([][]byte, len(items))
	for i, item := range items {
		chunk, ok := item.([]byte)
		if !ok || chunk == nil {
			return nil, fmt.Errorf("chunks[%d]: expected Uint8Array", i)
		}
		out[i] = bytes.Clone(chunk)
	}
	return out, nil
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
	for i, arg := range args {
		value, err := s.rt.FromGo(arg)
		if err != nil {
			s.rt.ReleaseCallData()
			return nil, nil, 0, false, err
		}
		values[i] = value
	}
	stop := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			s.rt.Interrupt("caller")
		case <-timer.C:
			if ctx.Err() != nil {
				s.rt.Interrupt("caller")
			} else {
				s.rt.Interrupt("timeout")
			}
		case <-stop:
		}
	}()
	result, err := s.rt.Call(hook, values...)
	close(stop)
	<-exited
	logs, dropped := s.logs.snapshot()
	if ctx.Err() != nil {
		return nil, logs, dropped, true, ctx.Err()
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "alloc_limit", Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		return nil, logs, dropped, false, &CallError{Kind: KindResponseTooLarge, Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ClearInterrupt()
	if err != nil {
		wrapped := wrapEngineError(err)
		if call, ok := wrapped.(*CallError); ok {
			call.Logs = logs
			call.DroppedLogs = dropped
		}
		s.rt.ReleaseCallData()
		return nil, logs, dropped, false, wrapped
	}
	if _, _, ok := moejs.PromiseResult(result); ok {
		s.rt.ReleaseCallData()
		return nil, logs, dropped, false, &CallError{Kind: KindShape, Detail: "hook returned a promise", Logs: logs, DroppedLogs: dropped}
	}
	out, err := s.rt.ToGo(result)
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		return nil, logs, dropped, false, &CallError{Kind: KindResponseTooLarge, Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "alloc_limit", Detail: err.Error(), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ReleaseCallData()
	if err != nil {
		wrapped := wrapEngineError(err)
		if call, ok := wrapped.(*CallError); ok {
			call.Logs = logs
			call.DroppedLogs = dropped
		}
		return nil, logs, dropped, false, wrapped
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
