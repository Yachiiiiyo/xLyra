package jsplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Yachiiiiyo/moejs"
)

var (
	errHookTimeout     = errors.New("hook timeout")
	errReturnedPromise = errors.New("hook returned a promise")
)

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
	StepsUsed          int64
	StepLimitHits      int64
	Interrupts         int64
	ICEntries          int
	PendingJobs        int
	RegisterStackBytes int64
}

// ModuleAnalysis is a pure-Go static security and complexity summary of a
// compiled plugin module, computed directly from bytecode without executing code.
type ModuleAnalysis struct {
	Exports           []string
	Imports           []string
	Globals           []string
	GlobalsRead       []string
	GlobalsWritten    []string
	UsesEval          bool
	UsesFunctionCtor  bool
	UsesDynamicImport bool
	UsesImportMeta    bool
	UsesAsync         bool
	UsesGenerators    bool
	UsesWith          bool
	FunctionCount     int
	InstructionCount  int
	BytecodeWords     int
	ConstantCount     int
	RegExpCount       int
	MaxRegs           int
	MaxHandlers       int
	MaxLine           int
}

func fromMoejsAnalysis(a moejs.ModuleAnalysis) ModuleAnalysis {
	return ModuleAnalysis{
		Exports:           slices.Clone(a.Exports),
		Imports:           slices.Clone(a.Imports),
		Globals:           slices.Clone(a.Globals),
		GlobalsRead:       slices.Clone(a.GlobalsRead),
		GlobalsWritten:    slices.Clone(a.GlobalsWritten),
		UsesEval:          a.UsesEval,
		UsesFunctionCtor:  a.UsesFunctionCtor,
		UsesDynamicImport: a.UsesDynamicImport,
		UsesImportMeta:    a.UsesImportMeta,
		UsesAsync:         a.UsesAsync,
		UsesGenerators:    a.UsesGenerators,
		UsesWith:          a.UsesWith,
		FunctionCount:     a.FunctionCount,
		InstructionCount:  a.InstructionCount,
		BytecodeWords:     a.BytecodeWords,
		ConstantCount:     a.ConstantCount,
		RegExpCount:       a.RegExpCount,
		MaxRegs:           a.MaxRegs,
		MaxHandlers:       a.MaxHandlers,
		MaxLine:           a.MaxLine,
	}
}

func cloneModuleAnalysis(a ModuleAnalysis) ModuleAnalysis {
	a.Exports = slices.Clone(a.Exports)
	a.Imports = slices.Clone(a.Imports)
	a.Globals = slices.Clone(a.Globals)
	a.GlobalsRead = slices.Clone(a.GlobalsRead)
	a.GlobalsWritten = slices.Clone(a.GlobalsWritten)
	return a
}

// AnalyzeSource compiles, verifies, and statically analyzes JavaScript ES
// module source without executing any code.
func AnalyzeSource(name, source string) (ModuleAnalysis, error) {
	mod, err := moejs.Compile(name, source)
	if err != nil {
		return ModuleAnalysis{}, err
	}
	if err := mod.Verify(); err != nil {
		return ModuleAnalysis{}, fmt.Errorf("bytecode verify: %w", err)
	}
	return fromMoejsAnalysis(mod.Analyze()), nil
}

// AnalyzeTypeScriptSource compiles (via in-parser type stripping), verifies,
// and statically analyzes TypeScript ES module source without executing any code.
func AnalyzeTypeScriptSource(name, source string) (ModuleAnalysis, error) {
	mod, err := moejs.CompileTS(name, source)
	if err != nil {
		return ModuleAnalysis{}, err
	}
	if err := mod.Verify(); err != nil {
		return ModuleAnalysis{}, fmt.Errorf("bytecode verify: %w", err)
	}
	return fromMoejsAnalysis(mod.Analyze()), nil
}

// program is one compiled plugin module. It is safe to load from many runtimes.
type program struct {
	mod      *moejs.Module
	hooks    map[string]moejs.Hook
	analysis ModuleAnalysis
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
	return buildProgram(mod, hookNames...)
}

func compileProgramTS(name, source string, hookNames ...string) (*program, error) {
	mod, err := moejs.CompileTS(name, source)
	if err != nil {
		return nil, err
	}
	return buildProgram(mod, hookNames...)
}

func buildProgram(mod *moejs.Module, hookNames ...string) (*program, error) {
	if err := mod.Verify(); err != nil {
		return nil, fmt.Errorf("bytecode verify: %w", err)
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
	return &program{
		mod:      mod,
		hooks:    hooks,
		analysis: fromMoejsAnalysis(mod.Analyze()),
	}, nil
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
	rt                  *moejs.Runtime
	logs                *callLog
	env                 callEnv
	liveDebt            int64
	callsSinceCheck     int
	hasCallStats        bool
	lastStepsUsed       int64
	lastAllocatedBytes  int64
	extraAllocLimitHits int64
	extraStepLimitHits  int64
	recordedAllocHits   int64
	recordedStepHits    int64
	recordedInterrupts  int64
}

func (s *session) setMaxSteps(n int64) {
	s.rt.SetMaxSteps(n)
}

// streamFilterHandle holds opaque moejs values for one open StreamFilterSession.
type streamFilterHandle struct {
	filterObj moejs.Value
	onEventFn moejs.Value
	onFlushFn moejs.Value
	hasFlush  bool
	lastLive  int64
}

func (h *streamFilterHandle) clear() {
	if h == nil {
		return
	}
	h.filterObj = moejs.Undefined()
	h.onEventFn = moejs.Undefined()
	h.onFlushFn = moejs.Undefined()
	h.hasFlush = false
	h.lastLive = 0
}

func addSaturating(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func (s *session) checkLiveLimit(allocDelta int64) error {
	limit := s.rt.MaxLiveBytes()
	if limit <= 0 {
		return nil
	}
	if allocDelta > 0 {
		s.liveDebt = addSaturating(s.liveDebt, allocDelta)
	}
	s.callsSinceCheck++
	if s.callsSinceCheck < liveCheckCallInterval && addSaturating(s.rt.Stats().LiveBytes, s.liveDebt) <= limit {
		return nil
	}
	s.liveDebt = 0
	s.callsSinceCheck = 0
	return s.rt.CheckLiveLimit()
}

func (s *session) forceCheckLiveLimit() error {
	limit := s.rt.MaxLiveBytes()
	if limit <= 0 {
		return nil
	}
	s.liveDebt = 0
	s.callsSinceCheck = 0
	return s.rt.CheckLiveLimit()
}

func (s *session) checkLiveLimitFilter(allocDelta int64, h *streamFilterHandle) error {
	limit := s.rt.MaxLiveBytes()
	if limit <= 0 {
		return nil
	}
	if allocDelta > 0 {
		s.liveDebt = addSaturating(s.liveDebt, allocDelta)
	}
	s.callsSinceCheck++
	baseLive := s.rt.Stats().LiveBytes
	if h != nil && h.lastLive > baseLive {
		baseLive = h.lastLive
	}
	if s.callsSinceCheck < liveCheckCallInterval && addSaturating(baseLive, s.liveDebt) <= limit {
		return nil
	}
	s.liveDebt = 0
	s.callsSinceCheck = 0
	var roots [3]moejs.Value
	n := 0
	if h != nil {
		if h.filterObj.IsObject() {
			roots[n] = h.filterObj
			n++
		}
		if h.onEventFn.IsObject() {
			roots[n] = h.onEventFn
			n++
		}
		if h.hasFlush && h.onFlushFn.IsObject() {
			roots[n] = h.onFlushFn
			n++
		}
	}
	err := s.rt.CheckLiveLimitWith(roots[:n]...)
	if h != nil {
		h.lastLive = s.rt.Stats().LiveBytes
	}
	return err
}

func (s *session) stats() RuntimeStats {
	st := s.rt.Stats()
	allocatedBytes := st.AllocatedBytes
	stepsUsed := st.StepsUsed
	if s.hasCallStats {
		allocatedBytes = s.lastAllocatedBytes
		stepsUsed = s.lastStepsUsed
	}
	return RuntimeStats{
		AllocatedBytes:     allocatedBytes,
		LiveBytes:          st.LiveBytes,
		AllocLimitHits:     addSaturating(st.AllocLimitHits, s.extraAllocLimitHits),
		StepsUsed:          stepsUsed,
		StepLimitHits:      addSaturating(st.StepLimitHits, s.extraStepLimitHits),
		Interrupts:         st.Interrupts,
		ICEntries:          st.ICEntries,
		PendingJobs:        st.PendingJobs,
		RegisterStackBytes: st.RegisterStackBytes,
	}
}

func (s *session) collectStatsDelta() (snap RuntimeStats, allocHitsDelta, stepHitsDelta, interruptDelta int64) {
	snap = s.stats()
	if snap.AllocLimitHits > s.recordedAllocHits {
		allocHitsDelta = snap.AllocLimitHits - s.recordedAllocHits
		s.recordedAllocHits = snap.AllocLimitHits
	}
	if snap.StepLimitHits > s.recordedStepHits {
		stepHitsDelta = snap.StepLimitHits - s.recordedStepHits
		s.recordedStepHits = snap.StepLimitHits
	}
	if snap.Interrupts > s.recordedInterrupts {
		interruptDelta = snap.Interrupts - s.recordedInterrupts
		s.recordedInterrupts = snap.Interrupts
	}
	return snap, allocHitsDelta, stepHitsDelta, interruptDelta
}

func classifySessionError(err error, runCtx context.Context, capturedStack string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return allocLimitCallError(err, nil, 0)
	}
	if errors.Is(err, moejs.ErrStepLimit) {
		return stepLimitCallError(err, nil, 0)
	}
	if errors.Is(err, moejs.ErrLiveLimit) {
		return &CallError{Kind: KindHeapLimit, Interrupt: "live_limit", Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err}
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		return &CallError{Kind: KindResponseTooLarge, Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		if errors.Is(err, errHookTimeout) || (runCtx != nil && errors.Is(context.Cause(runCtx), errHookTimeout)) {
			reason = "timeout"
		}
		return &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Err: err}
	}
	if runCtx != nil && errors.Is(context.Cause(runCtx), errHookTimeout) {
		return &CallError{Kind: KindTimeout, Interrupt: "timeout", Detail: "interrupted: timeout", Err: errHookTimeout}
	}
	return wrapEngineError(err, capturedStack)
}

func newSession(p *program, maxSteps int64) (*session, error) {
	if maxSteps < 0 {
		maxSteps = 0
	}
	logs := &callLog{}
	rt := moejs.NewRuntime(moejs.Options{
		DisableDynamicCode: true,
		MutableIntrinsics:  false,
		TimeZone:           time.UTC,
		MaxAllocBytes:      MaxAllocBytes,
		MaxResultBytes:     MaxResultBytes,
		MaxResultNodes:     MaxResultNodes,
		MaxLiveBytes:       MaxLiveBytes,
		MaxSteps:           maxSteps,
	})
	sess := &session{rt: rt, logs: logs}
	if err := installHost(sess); err != nil {
		return nil, err
	}
	loadCtx, cancel := context.WithTimeoutCause(context.Background(), loadHookTimeout, errHookTimeout)
	defer cancel()
	var excStack string
	loadErr := rt.RunContext(loadCtx, func(rt *moejs.Runtime) error {
		err := rt.Load(p.mod)
		if err != nil {
			excStack = captureExceptionStack(rt, err)
		}
		return err
	})
	cancel()
	if loadErr != nil {
		return nil, classifySessionError(loadErr, loadCtx, excStack)
	}
	rt.ClearInterrupt()
	rt.ReleaseCallData()
	if err := rt.CheckLiveLimit(); err != nil {
		return nil, classifySessionError(err, nil, "")
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

func newOwnedUint8ArrayValue(r *moejs.Realm, data []byte) (moejs.Value, error) {
	if data == nil {
		data = []byte{}
	}
	o, err := r.NewUint8Array(data)
	if err != nil {
		return moejs.Undefined(), err
	}
	return moejs.ObjectValue(o), nil
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
		if _, isStr := any(out).(string); !isStr {
			if est := estimateGoValueBytes(out); est > 0 {
				if err := r.ChargeAlloc(est); err != nil {
					return moejs.Undefined(), err
				}
			}
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
		return newOwnedUint8ArrayValue(r, []byte(text))
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
		return newOwnedUint8ArrayValue(r, out)
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
		out, err := argConcatBytes(r, args, 0)
		if err != nil {
			return moejs.Undefined(), err
		}
		return newOwnedUint8ArrayValue(r, out)
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
		return newOwnedUint8ArrayValue(r, out)
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
		return newOwnedUint8ArrayValue(r, out)
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
		return newOwnedUint8ArrayValue(r, out)
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
		return newOwnedUint8ArrayValue(r, out)
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
		return newOwnedUint8ArrayValue(r, out)
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
		if err := r.ChargeAlloc(estimateGoValueBytes(val)); err != nil {
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
		if err := r.ChargeAlloc(estimateGoValueBytes(keys)); err != nil {
			return moejs.Undefined(), err
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
	if raw.IsUint8Array() {
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
	if raw.IsUint8Array() {
		return nil, errors.New("detached byte buffer")
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
	if elem.IsUint8Array() {
		return nil, fmt.Errorf("chunks[%d]: detached byte buffer", i)
	}
	return nil, fmt.Errorf("chunks[%d]: expected Uint8Array", i)
}

func argConcatBytes(r *moejs.Realm, args []moejs.Value, index int) ([]byte, error) {
	raw := moejs.Arg(args, index)
	if !raw.IsObject() || raw.AsObject().ClassName() != "Array" {
		return nil, errors.New("expected array of Uint8Array")
	}
	o := raw.AsObject()
	if o.IsDenseArray() {
		elems := o.Elements()
		if len(elems) > maxConcatChunks {
			return nil, fmt.Errorf("too many chunks: %d exceeds %d", len(elems), maxConcatChunks)
		}
		total := 0
		for i, elem := range elems {
			if i&1023 == 0 {
				if err := r.CheckInterrupt(); err != nil {
					return nil, err
				}
			}
			chunk, err := decodeStrictBytesChunk(elem, i)
			if err != nil {
				return nil, err
			}
			if len(chunk) > maxBinaryInputBytes-total {
				return nil, fmt.Errorf("concatenated bytes exceed %d bytes", maxBinaryInputBytes)
			}
			total += len(chunk)
		}
		if total == 0 {
			return []byte{}, nil
		}
		out := make([]byte, 0, total)
		for i, elem := range elems {
			if i&1023 == 0 {
				if err := r.CheckInterrupt(); err != nil {
					return nil, err
				}
			}
			chunk, _ := elem.Uint8ArrayBytes()
			out = append(out, chunk...)
		}
		return out, nil
	}
	length := o.ArrayLength()
	if uint64(length) > maxConcatChunks {
		return nil, fmt.Errorf("too many chunks: %d exceeds %d", length, maxConcatChunks)
	}
	var out []byte
	for i := uint32(0); i < length; i++ {
		if i&255 == 0 {
			if err := r.CheckInterrupt(); err != nil {
				return nil, err
			}
		}
		elem, err := o.GetIndex(r, i)
		if err != nil {
			return nil, err
		}
		chunk, err := decodeStrictBytesChunk(elem, int(i))
		if err != nil {
			return nil, err
		}
		if len(chunk) > maxBinaryInputBytes-len(out) {
			return nil, fmt.Errorf("concatenated bytes exceed %d bytes", maxBinaryInputBytes)
		}
		out = append(out, chunk...)
	}
	if out == nil {
		return []byte{}, nil
	}
	return out, nil
}

const maxEstimateSteps = 65536

// estimateGoValueBytes returns a bounded, conservative byte estimate of a
// JSON-shaped Go value passed into or returned from moejs.FromGo. It mirrors
// moejs's live-heap estimator iteratively so liveDebt pacing and storage.get
// billing account for unmaterialized Go containers.
func estimateGoValueBytes(root any) int64 {
	if root == nil {
		return 0
	}
	limit := MaxAllocBytes + 1
	var total int64
	add := func(n int64) bool {
		if n <= 0 {
			return total >= limit
		}
		total = addSaturating(total, n)
		if total >= limit {
			total = limit
			return true
		}
		return false
	}
	var stack []any
	pushOrCount := func(v any) bool {
		switch x := v.(type) {
		case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return false
		case string:
			return add(32 + int64(len(x)))
		case json.Number:
			return add(32 + int64(len(x)))
		case []byte:
			if cap(x) == 0 {
				return false
			}
			return add(64 + int64(cap(x)))
		default:
			stack = append(stack, v)
			return false
		}
	}
	if pushOrCount(root) {
		return total
	}
	steps := 0
	for len(stack) > 0 {
		if steps >= maxEstimateSteps {
			add(MaxLiveBytes)
			return total
		}
		steps++
		n := len(stack) - 1
		cur := stack[n]
		stack[n] = nil
		stack = stack[:n]
		switch x := cur.(type) {
		case map[string]any:
			if x == nil {
				continue
			}
			if add(48 + int64(len(x))*48) {
				return total
			}
			for k, v := range x {
				steps++
				if add(int64(len(k))) || pushOrCount(v) {
					return total
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		case []any:
			if cap(x) == 0 {
				continue
			}
			if add(24 + int64(cap(x))*16) {
				return total
			}
			for _, elem := range x {
				steps++
				if pushOrCount(elem) {
					return total
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		case map[string]string:
			if x == nil {
				continue
			}
			if add(48 + int64(len(x))*48) {
				return total
			}
			for k, v := range x {
				steps++
				if add(int64(len(k)) + int64(len(v))) {
					return total
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		case []string:
			if cap(x) == 0 {
				continue
			}
			if add(24 + int64(cap(x))*16) {
				return total
			}
			for _, elem := range x {
				steps++
				if add(int64(len(elem))) {
					return total
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		case map[string][]string:
			if x == nil {
				continue
			}
			if add(48 + int64(len(x))*48) {
				return total
			}
			for k, list := range x {
				steps++
				if add(int64(len(k))) {
					return total
				}
				if cap(list) > 0 {
					if add(24 + int64(cap(list))*16) {
						return total
					}
					for _, elem := range list {
						steps++
						if add(int64(len(elem))) {
							return total
						}
						if steps >= maxEstimateSteps {
							add(MaxLiveBytes)
							return total
						}
					}
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		case []map[string]any:
			if cap(x) == 0 {
				continue
			}
			if add(24 + int64(cap(x))*8) {
				return total
			}
			for _, m := range x {
				steps++
				if m != nil {
					stack = append(stack, m)
				}
				if steps >= maxEstimateSteps {
					add(MaxLiveBytes)
					return total
				}
			}
		}
	}
	return total
}

func realmString(r *moejs.Realm, s string) (moejs.Value, error) {
	if s == "" {
		return moejs.String(""), nil
	}
	return r.FromGo(s)
}

func newObjectFromValue(r *moejs.Realm, tpl *moejs.ObjectTemplate, values ...moejs.Value) (moejs.Value, error) {
	o, err := r.NewObjectFrom(tpl, values...)
	if err != nil {
		return moejs.Undefined(), err
	}
	return moejs.ObjectValue(o), nil
}

func argToValue(r *moejs.Realm, arg any) (moejs.Value, int64, error) {
	switch v := arg.(type) {
	case StreamTransformEvent:
		val, err := streamEventToValue(r, v)
		return val, 0, err
	case *StreamTransformEvent:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		val, err := streamEventToValue(r, *v)
		return val, 0, err
	case StreamTransformContext:
		return streamCtxToValue(r, v)
	case *StreamTransformContext:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		return streamCtxToValue(r, *v)
	case SegmentItem:
		val, err := segmentItemToValue(r, v)
		return val, 0, err
	case *SegmentItem:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		val, err := segmentItemToValue(r, *v)
		return val, 0, err
	case SegmentFinding:
		val, err := segmentFindingToValue(r, v)
		return val, 0, err
	case *SegmentFinding:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		val, err := segmentFindingToValue(r, *v)
		return val, 0, err
	case []SegmentItem:
		val, err := segmentItemsToValue(r, v)
		return val, 0, err
	case []SegmentFinding:
		val, err := segmentFindingsToValue(r, v)
		return val, 0, err
	case InspectSegmentsBinding:
		return inspectBindingToValue(r, v)
	case *InspectSegmentsBinding:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		return inspectBindingToValue(r, *v)
	case InspectSegmentsContext:
		return inspectCtxToValue(r, v)
	case *InspectSegmentsContext:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		return inspectCtxToValue(r, *v)
	case InspectSegmentsInput:
		val, err := inspectInputToValue(r, v)
		return val, 0, err
	case *InspectSegmentsInput:
		if v == nil {
			return moejs.Null(), 0, nil
		}
		val, err := inspectInputToValue(r, *v)
		return val, 0, err
	default:
		val, err := r.FromGo(arg)
		if err != nil {
			return moejs.Undefined(), 0, err
		}
		return val, estimateGoValueBytes(arg), nil
	}
}

func streamEventToValue(r *moejs.Realm, e StreamTransformEvent) (moejs.Value, error) {
	dataVal, err := realmString(r, e.Data)
	if err != nil {
		return moejs.Undefined(), err
	}
	if e.Event == "" {
		return newObjectFromValue(r, tplStreamEventDataOnly, dataVal)
	}
	eventVal, err := realmString(r, e.Event)
	if err != nil {
		return moejs.Undefined(), err
	}
	return newObjectFromValue(r, tplStreamEventFull, dataVal, eventVal)
}

func streamCtxToValue(r *moejs.Realm, c StreamTransformContext) (moejs.Value, int64, error) {
	modelVal, err := realmString(r, c.Model)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	protocolVal, err := realmString(r, c.Protocol)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	siteTypeVal, err := realmString(r, c.SiteType)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	if len(c.Settings) == 0 {
		val, err := newObjectFromValue(r, tplStreamCtxBase, modelVal, protocolVal, siteTypeVal)
		return val, 0, err
	}
	settingsVal, err := r.FromGo(c.Settings)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	val, err := newObjectFromValue(r, tplStreamCtxWithSettings, modelVal, protocolVal, settingsVal, siteTypeVal)
	return val, estimateGoValueBytes(c.Settings), err
}

func segmentItemToValue(r *moejs.Realm, s SegmentItem) (moejs.Value, error) {
	fieldVal, err := realmString(r, s.Field)
	if err != nil {
		return moejs.Undefined(), err
	}
	idVal, err := realmString(r, s.ID)
	if err != nil {
		return moejs.Undefined(), err
	}
	textVal, err := realmString(r, s.Text)
	if err != nil {
		return moejs.Undefined(), err
	}
	if s.Role == "" {
		return newObjectFromValue(r, tplSegmentItemBase, fieldVal, idVal, textVal)
	}
	roleVal, err := realmString(r, s.Role)
	if err != nil {
		return moejs.Undefined(), err
	}
	return newObjectFromValue(r, tplSegmentItemWithRole, fieldVal, idVal, roleVal, textVal)
}

func segmentFindingToValue(r *moejs.Realm, f SegmentFinding) (moejs.Value, error) {
	segVal, err := realmString(r, f.Segment)
	if err != nil {
		return moejs.Undefined(), err
	}
	typeVal, err := realmString(r, f.Type)
	if err != nil {
		return moejs.Undefined(), err
	}
	hasSource := f.Source != ""
	hasScore := f.Score != 0
	switch {
	case hasSource && hasScore:
		srcVal, err := realmString(r, f.Source)
		if err != nil {
			return moejs.Undefined(), err
		}
		return newObjectFromValue(r, tplFindingFull, moejs.Int(int64(f.End)), moejs.Number(f.Score), segVal, srcVal, moejs.Int(int64(f.Start)), typeVal)
	case hasSource:
		srcVal, err := realmString(r, f.Source)
		if err != nil {
			return moejs.Undefined(), err
		}
		return newObjectFromValue(r, tplFindingSource, moejs.Int(int64(f.End)), segVal, srcVal, moejs.Int(int64(f.Start)), typeVal)
	case hasScore:
		return newObjectFromValue(r, tplFindingScore, moejs.Int(int64(f.End)), moejs.Number(f.Score), segVal, moejs.Int(int64(f.Start)), typeVal)
	default:
		return newObjectFromValue(r, tplFindingBase, moejs.Int(int64(f.End)), segVal, moejs.Int(int64(f.Start)), typeVal)
	}
}

func segmentItemsToValue(r *moejs.Realm, items []SegmentItem) (moejs.Value, error) {
	if len(items) == 0 {
		return moejs.ObjectValue(r.NewArrayFromSlice(nil)), nil
	}
	if err := r.ChargeAlloc(int64(len(items)) * 16); err != nil {
		return moejs.Undefined(), err
	}
	vals := make([]moejs.Value, len(items))
	for i, item := range items {
		val, err := segmentItemToValue(r, item)
		if err != nil {
			return moejs.Undefined(), err
		}
		vals[i] = val
	}
	return moejs.ObjectValue(r.NewArrayFromSlice(vals)), nil
}

func segmentFindingsToValue(r *moejs.Realm, items []SegmentFinding) (moejs.Value, error) {
	if len(items) == 0 {
		return moejs.ObjectValue(r.NewArrayFromSlice(nil)), nil
	}
	if err := r.ChargeAlloc(int64(len(items)) * 16); err != nil {
		return moejs.Undefined(), err
	}
	vals := make([]moejs.Value, len(items))
	for i, item := range items {
		val, err := segmentFindingToValue(r, item)
		if err != nil {
			return moejs.Undefined(), err
		}
		vals[i] = val
	}
	return moejs.ObjectValue(r.NewArrayFromSlice(vals)), nil
}

func inspectBindingToValue(r *moejs.Realm, b InspectSegmentsBinding) (moejs.Value, int64, error) {
	idVal, err := realmString(r, b.ID)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	paramsVal, err := r.FromGo(b.Params)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	val, err := newObjectFromValue(r, tplInspectBinding, idVal, paramsVal)
	return val, estimateGoValueBytes(b.Params), err
}

func inspectCtxToValue(r *moejs.Realm, c InspectSegmentsContext) (moejs.Value, int64, error) {
	bindingVal, bindingBytes, err := inspectBindingToValue(r, c.Binding)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	endpointVal, err := realmString(r, c.Endpoint)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	modelVal, err := realmString(r, c.Model)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	if c.Settings == nil {
		val, err := newObjectFromValue(r, tplInspectCtxBase, bindingVal, endpointVal, modelVal)
		return val, bindingBytes, err
	}
	settingsVal, err := r.FromGo(c.Settings)
	if err != nil {
		return moejs.Undefined(), 0, err
	}
	val, err := newObjectFromValue(r, tplInspectCtxWithSettings, bindingVal, endpointVal, modelVal, settingsVal)
	return val, addSaturating(bindingBytes, estimateGoValueBytes(c.Settings)), err
}

func inspectInputToValue(r *moejs.Realm, in InspectSegmentsInput) (moejs.Value, error) {
	findingsVal, err := segmentFindingsToValue(r, in.Findings)
	if err != nil {
		return moejs.Undefined(), err
	}
	segmentsVal, err := segmentItemsToValue(r, in.Segments)
	if err != nil {
		return moejs.Undefined(), err
	}
	return newObjectFromValue(r, tplInspectInput, findingsVal, segmentsVal)
}

const maxErrorDetailBytes = 4096

func allocLimitCallError(err error, logs []LogEntry, dropped int) *CallError {
	detail := clipUTF8Bytes(err.Error(), maxErrorDetailBytes)
	var lim *moejs.AllocLimitError
	if errors.As(err, &lim) && lim.Stack != "" {
		stack := clipUTF8Bytes(lim.Stack, maxErrorDetailBytes)
		detail = clipUTF8Bytes(detail+"\n"+stack, maxErrorDetailBytes)
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

func stepLimitCallError(err error, logs []LogEntry, dropped int) *CallError {
	detail := clipUTF8Bytes(err.Error(), maxErrorDetailBytes)
	var lim *moejs.StepLimitError
	if errors.As(err, &lim) && lim.Stack != "" {
		stack := clipUTF8Bytes(lim.Stack, maxErrorDetailBytes)
		detail = clipUTF8Bytes(detail+"\n"+stack, maxErrorDetailBytes)
	}
	return &CallError{
		Kind:        KindTimeout,
		Interrupt:   "step_limit",
		Detail:      detail,
		Err:         err,
		Logs:        logs,
		DroppedLogs: dropped,
	}
}

func captureExceptionStack(rt *moejs.Runtime, err error) string {
	var exc *moejs.Exception
	if errors.As(err, &exc) && exc.Stack == "" && rt != nil {
		return rt.StackTrace(exc)
	}
	return ""
}

func (s *session) runWithBudgets(maxAlloc int64, allocUsed *int64, maxSteps int64, stepsUsed *int64, fn func() error) error {
	rt := s.rt
	if maxAlloc > 0 {
		if *allocUsed > maxAlloc {
			s.extraAllocLimitHits++
			return &moejs.AllocLimitError{Limit: maxAlloc, Used: *allocUsed}
		}
		remAlloc := maxAlloc - *allocUsed
		if remAlloc <= 0 {
			// SetMaxAllocBytes(0) disables the budget in moejs; use 1 so zero-alloc
			// operations (e.g., plain ToGo) can still complete while any allocation
			// either trips immediately or is caught by the post-check below.
			remAlloc = 1
		}
		rt.SetMaxAllocBytes(remAlloc)
		defer rt.SetMaxAllocBytes(maxAlloc)
	}
	if maxSteps > 0 {
		if *stepsUsed > maxSteps {
			s.extraStepLimitHits++
			return &moejs.StepLimitError{Limit: maxSteps, Used: *stepsUsed}
		}
		remSteps := maxSteps - *stepsUsed
		if remSteps <= 0 {
			// SetMaxSteps(0) disables the budget in moejs; use 1 so zero-step
			// operations (e.g., plain ToGo or data-property Get) can complete while
			// any JS frame execution is rejected by the post-check below.
			remSteps = 1
		}
		rt.SetMaxSteps(remSteps)
		defer rt.SetMaxSteps(maxSteps)
	}
	prevAlloc := *allocUsed
	prevSteps := *stepsUsed
	err := fn()
	allocDelta := rt.AllocatedBytes()
	stepDelta := rt.StepsUsed()
	*allocUsed = addSaturating(prevAlloc, allocDelta)
	*stepsUsed = addSaturating(prevSteps, stepDelta)

	if errors.Is(err, moejs.ErrAllocLimit) {
		if maxAlloc > 0 {
			var lim *moejs.AllocLimitError
			if errors.As(err, &lim) {
				return &moejs.AllocLimitError{
					Limit:     maxAlloc,
					Used:      addSaturating(prevAlloc, lim.Used),
					Requested: lim.Requested,
					Stack:     lim.Stack,
				}
			}
		}
		return err
	}
	if errors.Is(err, moejs.ErrStepLimit) {
		if maxSteps > 0 {
			var lim *moejs.StepLimitError
			if errors.As(err, &lim) {
				return &moejs.StepLimitError{
					Limit: maxSteps,
					Used:  *stepsUsed,
					Stack: lim.Stack,
				}
			}
		}
		return err
	}
	if _, interrupted := interruptReason(err); interrupted {
		return err
	}
	if maxAlloc > 0 && *allocUsed > maxAlloc {
		s.extraAllocLimitHits++
		return &moejs.AllocLimitError{
			Limit:     maxAlloc,
			Used:      prevAlloc,
			Requested: allocDelta,
		}
	}
	if maxSteps > 0 && *stepsUsed > maxSteps {
		s.extraStepLimitHits++
		return &moejs.StepLimitError{
			Limit: maxSteps,
			Used:  *stepsUsed,
		}
	}
	return err
}

func (s *session) call(ctx context.Context, timeout time.Duration, hook moejs.Hook, args []any) (any, []LogEntry, int, bool, error) {
	return s.callWithEnv(ctx, timeout, hook, callEnv{}, args)
}

func (s *session) callWithEnv(ctx context.Context, timeout time.Duration, hook moejs.Hook, env callEnv, args []any) (any, []LogEntry, int, bool, error) {
	s.hasCallStats = true
	s.lastStepsUsed = 0
	s.lastAllocatedBytes = 0
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, false, err
	}
	s.logs.reset()

	values := make([]moejs.Value, len(args))
	r := s.rt.Realm()
	r.SetAllocBudget(MaxAllocBytes)
	var argErr error
	var unchargedGoBytes int64
	for i, arg := range args {
		var goBytes int64
		values[i], goBytes, argErr = argToValue(r, arg)
		if goBytes > 0 {
			unchargedGoBytes = addSaturating(unchargedGoBytes, goBytes)
		}
		if argErr != nil {
			break
		}
	}
	argErr = r.FinishOutermost(argErr)
	allocUsed := r.AllocatedBytes()
	s.lastAllocatedBytes = allocUsed
	if argErr != nil {
		s.rt.ReleaseCallData()
		if errors.Is(argErr, moejs.ErrAllocLimit) {
			return nil, nil, 0, true, allocLimitCallError(argErr, nil, 0)
		}
		return nil, nil, 0, false, argErr
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, timeout, errHookTimeout)
	defer cancel()
	env.ctx = callCtx
	s.env = env
	defer func() { s.env = callEnv{} }()

	maxSteps := s.rt.MaxSteps()
	var totalSteps int64
	var out any
	var excStack string
	err := s.rt.RunContext(callCtx, func(rt *moejs.Runtime) (runErr error) {
		defer func() {
			if runErr != nil {
				excStack = captureExceptionStack(rt, runErr)
			}
		}()
		var result moejs.Value
		if callErr := s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var err error
			result, err = rt.Call(hook, values...)
			return err
		}); callErr != nil {
			return callErr
		}
		if _, _, ok := moejs.PromiseResult(result); ok {
			return errReturnedPromise
		}
		return s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var toGoErr error
			out, toGoErr = rt.ToGo(result)
			return toGoErr
		})
	})
	cancel()
	s.lastStepsUsed = totalSteps
	s.lastAllocatedBytes = allocUsed
	callAlloc := addSaturating(allocUsed, unchargedGoBytes)
	logs, dropped := s.logs.snapshot()
	if ctx.Err() != nil {
		return nil, logs, dropped, true, ctx.Err()
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, allocLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrStepLimit) {
		return nil, logs, dropped, true, stepLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindResponseTooLarge, Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		if errors.Is(err, errHookTimeout) || errors.Is(context.Cause(callCtx), errHookTimeout) {
			reason = "timeout"
		}
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if err != nil && errors.Is(context.Cause(callCtx), errHookTimeout) {
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: "timeout", Detail: "interrupted: timeout", Err: errHookTimeout, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ClearInterrupt()
	if errors.Is(err, errReturnedPromise) {
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindShape, Detail: "hook returned a promise", Logs: logs, DroppedLogs: dropped}
	}
	if err != nil {
		wrapped := wrapEngineError(err, excStack)
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
	s.rt.ReleaseCallData()
	if liveErr := s.checkLiveLimit(callAlloc); liveErr != nil {
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "live_limit", Detail: clipUTF8Bytes(liveErr.Error(), maxErrorDetailBytes), Err: liveErr, Logs: logs, DroppedLogs: dropped}
	}
	return out, logs, dropped, false, nil
}

func (s *session) openStreamFilter(ctx context.Context, timeout time.Duration, hook moejs.Hook, env callEnv, ctxArg any) (*streamFilterHandle, []LogEntry, int, bool, error) {
	s.hasCallStats = true
	s.lastStepsUsed = 0
	s.lastAllocatedBytes = 0
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, false, err
	}
	s.logs.reset()

	r := s.rt.Realm()
	r.SetAllocBudget(MaxAllocBytes)
	ctxVal, unchargedGoBytes, argErr := argToValue(r, ctxArg)
	argErr = r.FinishOutermost(argErr)
	allocUsed := r.AllocatedBytes()
	s.lastAllocatedBytes = allocUsed
	if argErr != nil {
		s.rt.ReleaseCallData()
		if errors.Is(argErr, moejs.ErrAllocLimit) {
			return nil, nil, 0, true, allocLimitCallError(argErr, nil, 0)
		}
		return nil, nil, 0, false, argErr
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, timeout, errHookTimeout)
	defer cancel()
	env.ctx = callCtx
	s.env = env
	defer func() { s.env = callEnv{} }()

	maxSteps := s.rt.MaxSteps()
	var totalSteps int64
	var handle *streamFilterHandle
	var shapeErr *CallError
	var excStack string
	err := s.rt.RunContext(callCtx, func(rt *moejs.Runtime) (runErr error) {
		defer func() {
			if runErr != nil {
				excStack = captureExceptionStack(rt, runErr)
			}
		}()
		var filterObj moejs.Value
		if callErr := s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var err error
			filterObj, err = rt.Call(hook, ctxVal)
			return err
		}); callErr != nil {
			return callErr
		}
		if _, _, ok := moejs.PromiseResult(filterObj); ok {
			return errReturnedPromise
		}
		if !filterObj.IsObject() {
			shapeErr = shapeError("createStreamFilter must return an object")
			return nil
		}
		var onEventFn moejs.Value
		if getErr := s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var err error
			onEventFn, err = rt.Get(filterObj, "onEvent")
			return err
		}); getErr != nil {
			return getErr
		}
		if !onEventFn.IsObject() || !onEventFn.AsObject().IsCallable() {
			shapeErr = shapeError("createStreamFilter: onEvent must be a function")
			return nil
		}
		var onFlushFn moejs.Value
		if getErr := s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var err error
			onFlushFn, err = rt.Get(filterObj, "onFlush")
			return err
		}); getErr != nil {
			return getErr
		}
		hasFlush := false
		if !onFlushFn.IsUndefined() && !onFlushFn.IsNull() {
			if !onFlushFn.IsObject() || !onFlushFn.AsObject().IsCallable() {
				shapeErr = shapeError("createStreamFilter: onFlush must be a function")
				return nil
			}
			hasFlush = true
		}
		handle = &streamFilterHandle{
			filterObj: filterObj,
			onEventFn: onEventFn,
			onFlushFn: onFlushFn,
			hasFlush:  hasFlush,
		}
		return nil
	})
	cancel()
	s.lastStepsUsed = totalSteps
	s.lastAllocatedBytes = allocUsed
	callAlloc := addSaturating(allocUsed, unchargedGoBytes)
	logs, dropped := s.logs.snapshot()
	if ctx.Err() != nil {
		return nil, logs, dropped, true, ctx.Err()
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, allocLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrStepLimit) {
		return nil, logs, dropped, true, stepLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindResponseTooLarge, Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		if errors.Is(err, errHookTimeout) || errors.Is(context.Cause(callCtx), errHookTimeout) {
			reason = "timeout"
		}
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if err != nil && errors.Is(context.Cause(callCtx), errHookTimeout) {
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: "timeout", Detail: "interrupted: timeout", Err: errHookTimeout, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ClearInterrupt()
	if errors.Is(err, errReturnedPromise) {
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindShape, Detail: "hook returned a promise", Logs: logs, DroppedLogs: dropped}
	}
	if shapeErr != nil {
		shapeErr.Logs = logs
		shapeErr.DroppedLogs = dropped
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimit(callAlloc) != nil
		return nil, logs, dropped, discard, shapeErr
	}
	if err != nil {
		wrapped := wrapEngineError(err, excStack)
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
	s.rt.ReleaseCallData()
	if liveErr := s.checkLiveLimitFilter(callAlloc, handle); liveErr != nil {
		handle.clear()
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "live_limit", Detail: clipUTF8Bytes(liveErr.Error(), maxErrorDetailBytes), Err: liveErr, Logs: logs, DroppedLogs: dropped}
	}
	return handle, logs, dropped, false, nil
}

func (s *session) callStreamFilter(ctx context.Context, timeout time.Duration, handle *streamFilterHandle, isFlush bool, env callEnv, event StreamTransformEvent) (any, []LogEntry, int, bool, error) {
	s.hasCallStats = true
	s.lastStepsUsed = 0
	s.lastAllocatedBytes = 0
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, false, err
	}
	s.logs.reset()

	var allocUsed int64
	var unchargedGoBytes int64
	var eventVal moejs.Value
	fn := handle.onEventFn
	if isFlush {
		fn = handle.onFlushFn
	} else {
		r := s.rt.Realm()
		r.SetAllocBudget(MaxAllocBytes)
		var argErr error
		eventVal, unchargedGoBytes, argErr = argToValue(r, event)
		argErr = r.FinishOutermost(argErr)
		allocUsed = r.AllocatedBytes()
		s.lastAllocatedBytes = allocUsed
		if argErr != nil {
			s.rt.ReleaseCallData()
			if errors.Is(argErr, moejs.ErrAllocLimit) {
				return nil, nil, 0, true, allocLimitCallError(argErr, nil, 0)
			}
			return nil, nil, 0, false, argErr
		}
	}

	callCtx, cancel := context.WithTimeoutCause(ctx, timeout, errHookTimeout)
	defer cancel()
	env.ctx = callCtx
	s.env = env
	defer func() { s.env = callEnv{} }()

	maxSteps := s.rt.MaxSteps()
	var totalSteps int64
	var out any
	var excStack string
	err := s.rt.RunContext(callCtx, func(rt *moejs.Runtime) (runErr error) {
		defer func() {
			if runErr != nil {
				excStack = captureExceptionStack(rt, runErr)
			}
		}()
		var result moejs.Value
		if callErr := s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var err error
			if isFlush {
				result, err = rt.CallFunction(fn, handle.filterObj)
			} else {
				result, err = rt.CallFunction(fn, handle.filterObj, eventVal)
			}
			return err
		}); callErr != nil {
			return callErr
		}
		if _, _, ok := moejs.PromiseResult(result); ok {
			return errReturnedPromise
		}
		return s.runWithBudgets(MaxAllocBytes, &allocUsed, maxSteps, &totalSteps, func() error {
			var toGoErr error
			out, toGoErr = rt.ToGo(result)
			return toGoErr
		})
	})
	cancel()
	s.lastStepsUsed = totalSteps
	s.lastAllocatedBytes = allocUsed
	callAlloc := addSaturating(allocUsed, unchargedGoBytes)
	logs, dropped := s.logs.snapshot()
	if ctx.Err() != nil {
		return nil, logs, dropped, true, ctx.Err()
	}
	if errors.Is(err, moejs.ErrAllocLimit) {
		return nil, logs, dropped, true, allocLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrStepLimit) {
		return nil, logs, dropped, true, stepLimitCallError(err, logs, dropped)
	}
	if errors.Is(err, moejs.ErrResultTooLarge) {
		s.rt.ClearInterrupt()
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimitFilter(callAlloc, handle) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindResponseTooLarge, Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if reason, interrupted := interruptReason(err); interrupted {
		if errors.Is(err, errHookTimeout) || errors.Is(context.Cause(callCtx), errHookTimeout) {
			reason = "timeout"
		}
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: reason, Detail: "interrupted: " + reason, Err: err, Logs: logs, DroppedLogs: dropped}
	}
	if err != nil && errors.Is(context.Cause(callCtx), errHookTimeout) {
		return nil, logs, dropped, true, &CallError{Kind: KindTimeout, Interrupt: "timeout", Detail: "interrupted: timeout", Err: errHookTimeout, Logs: logs, DroppedLogs: dropped}
	}
	s.rt.ClearInterrupt()
	if errors.Is(err, errReturnedPromise) {
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimitFilter(callAlloc, handle) != nil
		return nil, logs, dropped, discard, &CallError{Kind: KindShape, Detail: "hook returned a promise", Logs: logs, DroppedLogs: dropped}
	}
	if err != nil {
		wrapped := wrapEngineError(err, excStack)
		if call, ok := wrapped.(*CallError); ok {
			call.Logs = logs
			call.DroppedLogs = dropped
		}
		if fatalRuntime(wrapped) {
			return nil, logs, dropped, true, wrapped
		}
		s.rt.ReleaseCallData()
		discard := s.checkLiveLimitFilter(callAlloc, handle) != nil
		return nil, logs, dropped, discard, wrapped
	}
	s.rt.ReleaseCallData()
	if liveErr := s.checkLiveLimitFilter(callAlloc, handle); liveErr != nil {
		return nil, logs, dropped, true, &CallError{Kind: KindHeapLimit, Interrupt: "live_limit", Detail: clipUTF8Bytes(liveErr.Error(), maxErrorDetailBytes), Err: liveErr, Logs: logs, DroppedLogs: dropped}
	}
	return out, logs, dropped, false, nil
}

func (s *session) finishStreamFilter(handle *streamFilterHandle, poisoned bool) bool {
	if handle != nil {
		handle.clear()
	}
	if poisoned {
		return true
	}
	s.rt.ClearInterrupt()
	s.rt.ReleaseCallData()
	return s.forceCheckLiveLimit() != nil
}

func (s *session) exportMeta() (map[string]any, error) {
	value, ok := s.rt.Export("meta")
	if !ok {
		return nil, fmt.Errorf("meta export is missing")
	}
	metaCtx, cancel := context.WithTimeoutCause(context.Background(), loadHookTimeout, errHookTimeout)
	defer cancel()
	var out any
	var excStack string
	err := s.rt.RunContext(metaCtx, func(rt *moejs.Runtime) error {
		var toGoErr error
		out, toGoErr = rt.ToGo(value)
		if toGoErr != nil {
			excStack = captureExceptionStack(rt, toGoErr)
		}
		return toGoErr
	})
	cancel()
	s.rt.ClearInterrupt()
	s.rt.ReleaseCallData()
	if err != nil {
		return nil, classifySessionError(err, metaCtx, excStack)
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

func wrapEngineError(err error, capturedStack string) error {
	var call *CallError
	if errors.As(err, &call) {
		return call
	}
	var exception *moejs.Exception
	if errors.As(err, &exception) {
		rawDetail := exception.Message()
		if rawDetail == "" {
			rawDetail = exception.Error()
		}
		detail := clipUTF8Bytes(rawDetail, maxErrorDetailBytes)
		if exception.Stack != "" {
			if rawDetail == "" || strings.Contains(exception.Stack, rawDetail) {
				detail = clipUTF8Bytes(exception.Stack, maxErrorDetailBytes)
			} else {
				stack := clipUTF8Bytes(exception.Stack, maxErrorDetailBytes)
				detail = clipUTF8Bytes(detail+"\n"+stack, maxErrorDetailBytes)
			}
		} else if capturedStack != "" {
			if rawDetail == "" || strings.Contains(capturedStack, rawDetail) {
				detail = clipUTF8Bytes(capturedStack, maxErrorDetailBytes)
			} else {
				rtStack := clipUTF8Bytes(capturedStack, maxErrorDetailBytes)
				detail = clipUTF8Bytes(detail+"\n"+rtStack, maxErrorDetailBytes)
			}
		}
		return &CallError{Kind: KindException, Detail: detail, Err: err}
	}
	var internal *moejs.InternalError
	if errors.As(err, &internal) {
		return &CallError{Kind: KindInternal, Detail: clipUTF8Bytes(internal.Error(), maxErrorDetailBytes), Err: err}
	}
	return &CallError{Kind: KindException, Detail: clipUTF8Bytes(err.Error(), maxErrorDetailBytes), Err: err}
}
