package jsplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// HookGatewayPipelineFixture is the end-to-end fixture hook name for a
// gatewayPipeline plugin (runs native rules, inspectSegments, transformation
// and response restoration).
const HookGatewayPipelineFixture = "gatewayPipeline"

var gatewayPipelineFixtureRunner func(ctx context.Context, plugin *Plugin, fixture Fixture) error

// RegisterGatewayPipelineFixtureRunner installs the full pipeline fixture runner
// from internal/textpipeline without introducing an import cycle.
func RegisterGatewayPipelineFixtureRunner(fn func(ctx context.Context, plugin *Plugin, fixture Fixture) error) {
	gatewayPipelineFixtureRunner = fn
}

// FixturePipelineBinding configures the binding parameters and host options of
// a gatewayPipeline end-to-end fixture.
type FixturePipelineBinding struct {
	Params      map[string]any `json:"params,omitempty"`
	HostOptions map[string]any `json:"hostOptions,omitempty"`
}

// FixturePipelineUpstream is a simulated upstream response (buffered or SSE chunks).
type FixturePipelineUpstream struct {
	Status      int      `json:"status,omitempty"`
	ContentType string   `json:"contentType,omitempty"`
	Body        string   `json:"body,omitempty"`
	JSON        any      `json:"json,omitempty"`
	SSEChunks   []string `json:"sseChunks,omitempty"`
}

// Fixture is one recorded sample. Hook selects the export for a site_provider
// or multi-hook sample; single-hook kinds may leave it empty.
type Fixture struct {
	Name string `json:"name"`
	Hook string `json:"hook,omitempty"`
	// File is the package path, such as fixtures/ok.json. It is not part of the sample JSON.
	File              string                    `json:"-"`
	Ctx               fixtureContext            `json:"ctx"`
	Storage           map[string]any            `json:"storage,omitempty"`
	Responses         []FixtureResponse         `json:"responses"`
	Expect            fixtureExpectation        `json:"expect"`
	Payload           map[string]any            `json:"payload"`
	Input             any                       `json:"input"`
	Candidate         fixtureCandidate          `json:"candidate"`
	Response          fixtureUpstream           `json:"response"`
	Binding           *FixturePipelineBinding   `json:"binding,omitempty"`
	Endpoint          string                    `json:"endpoint,omitempty"`
	Upstream          *FixturePipelineUpstream  `json:"upstream,omitempty"`
}

type fixtureCandidate struct {
	SiteType      string `json:"siteType"`
	BaseURL       string `json:"baseURL"`
	UpstreamName  string `json:"upstreamName"`
	UpstreamModel string `json:"upstreamModel"`
}

type fixtureUpstream struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	JSON   any    `json:"json"`
}

type fixtureContext struct {
	SiteType       string            `json:"siteType"`
	BaseURL        string            `json:"baseURL"`
	CredentialType string            `json:"credentialType"`
	Now            int64             `json:"now"`
	Account        map[string]string `json:"account,omitempty"`
	Params         map[string]string `json:"params,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Settings       map[string]any    `json:"settings,omitempty"`
	Event          string            `json:"event,omitempty"`
	BindingID      string            `json:"bindingId,omitempty"`
	// Inputs and State stand in for what the admin picked and the binding's
	// saved state in an automation fixture.
	Inputs     map[string]any    `json:"inputs"`
	State      map[string]any    `json:"state"`
	Model      string            `json:"model"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Stream     bool              `json:"stream"`
	Protocol   string            `json:"protocol"`
	SessionKey string            `json:"sessionKey,omitempty"`
	ModelMeta  map[string]any    `json:"modelMeta,omitempty"`
	Credential map[string]string `json:"credential,omitempty"`
}

type FixtureResponse struct {
	Match  FixtureMatch `json:"match"`
	Status int          `json:"status"`
	Body   string       `json:"body"`
	JSON   any          `json:"json"`
	Error  string       `json:"error"`
}

type FixtureMatch struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	PathPrefix string `json:"pathPrefix"`
	Endpoint   string `json:"endpoint,omitempty"`
	Sign       *bool  `json:"sign,omitempty"`
}

type fixtureExpectation struct {
	Requests []FixtureMatch `json:"requests"`
	Result   map[string]any `json:"result"`
	Storage  map[string]any `json:"storage,omitempty"`
	Error    string         `json:"error"`
	Decode   map[string]any `json:"decode"`
	Request  map[string]any `json:"request"`
	Parse    map[string]any `json:"parse"`
	// Sign is compared with the signRequest result (stringToSign, algorithm, header, ...).
	Sign map[string]any `json:"sign"`
	// ParseError is compared with the parseError result (contentType, body).
	ParseError map[string]any `json:"parseError"`
	// GatewayPipeline e2e fixture expectations.
	UpstreamPayload    map[string]any `json:"upstreamPayload,omitempty"`
	DownstreamResponse any            `json:"downstreamResponse,omitempty"`
	DownstreamSSE      string         `json:"downstreamSSE,omitempty"`
	Status             int            `json:"status,omitempty"`
	ErrorCode          string         `json:"errorCode,omitempty"`
}

// SelfTest runs the package fixtures inside the sandbox. A site_provider with
// no samples still passes: enabling the package is enough. So does a gateway
// pipeline that exports no hook, whose rules the manifest check already compiled.
func (p *Plugin) SelfTest(ctx context.Context) error {
	if len(p.fixtures) == 0 {
		if p.Manifest.DeclaresSiteType() {
			return nil
		}
		if p.Manifest.DeclaresGatewayPipeline() && len(p.program.knownHooks()) == 0 {
			return nil
		}
		return fmt.Errorf("%s: no fixtures", p.Manifest.ID)
	}
	for _, fixture := range p.fixtures {
		if err := p.RunFixture(ctx, fixture); err != nil {
			name := fixture.Name
			if name == "" {
				name = fixture.File
			}
			return fmt.Errorf("%s fixture %q: %w", p.Manifest.ID, name, err)
		}
	}
	return nil
}

// RunFixture runs one fixture and returns the first mismatch. The developer
// CLI uses it to report fixtures one by one; SelfTest stops at the first failure.
func (p *Plugin) RunFixture(ctx context.Context, fixture Fixture) error {
	if err := p.checkFixtureHook(fixture); err != nil {
		return err
	}
	memStore, err := NewMemoryStorage(fixture.Storage)
	if err != nil {
		return fmt.Errorf("%s: %w", fixtureRef(fixture), err)
	}
	callCtx := WithCallStorage(ctx, memStore)
	if len(fixture.Ctx.Settings) > 0 {
		callCtx = WithSiteSettings(callCtx, fixture.Ctx.Settings)
	}
	if err := p.dispatchFixture(callCtx, fixture); err != nil {
		return err
	}
	if fixture.Expect.Storage != nil {
		if err := matchFixtureStorage(memStore, fixture.Expect.Storage); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) dispatchFixture(ctx context.Context, fixture Fixture) error {
	if fixture.Hook != "" {
		return p.runHookFixture(ctx, fixture)
	}
	if p.Manifest.Kind == "" {
		if p.Manifest.DeclaresProtocol() {
			return p.runProtocolFixture(ctx, fixture)
		}
		if p.Manifest.DeclaresAutomation() {
			return p.runEventFixtureHook(ctx, fixture, "onEvent")
		}
		return fmt.Errorf("%s: fixture.hook is required for plugins without kind", fixtureRef(fixture))
	}
	switch p.Manifest.Kind {
	case KindQuotaProbe:
		return p.runProbeFixture(ctx, fixture)
	case KindProtocol:
		return p.runProtocolFixture(ctx, fixture)
	}
	spec, ok := lookupKind(p.Manifest.Kind)
	if !ok {
		return fmt.Errorf("selftest does not support kind %q", p.Manifest.Kind)
	}
	switch spec.Family {
	case FamilyStepped:
		return p.runSteppedFixture(ctx, fixture)
	case FamilyOneShot, FamilyEvent:
		return p.runOneShotFixture(ctx, fixture)
	}
	return fmt.Errorf("selftest does not support kind %q", p.Manifest.Kind)
}

func fixtureRef(fixture Fixture) string {
	if fixture.File != "" {
		return fixture.File
	}
	if fixture.Name != "" {
		return fixture.Name
	}
	return "fixture"
}

func isValidSiteProviderFixtureHook(hook string) bool {
	switch hook {
	case "probe", "listModels", "check", "classify", "classifyError", "rewritePayload", HookTransformStream, HookCreateStreamFilter,
		"parsePricing", "describeModels",
		"startLogin", "pollLogin", "refreshTokens", "summarizeAccount",
		HookSignRequest, "onEvent", "handle", "onSchedule":
		return true
	default:
		return false
	}
}

// checkFixtureHook enforces the site_provider hook field. Other single-hook kinds reject a hook,
// except kind=protocol may declare transformStream or createStreamFilter fixtures when exported.
func (p *Plugin) checkFixtureHook(fixture Fixture) error {
	ref := fixtureRef(fixture)
	if !p.Manifest.DeclaresSiteType() && p.Manifest.Kind != "" {
		if fixture.Hook != "" {
			if p.Manifest.Kind == KindProtocol && (fixture.Hook == HookTransformStream || fixture.Hook == HookCreateStreamFilter) {
				if !p.HasHook(fixture.Hook) {
					return fmt.Errorf("%s: hook %q is not exported", ref, fixture.Hook)
				}
				return nil
			}
			return fmt.Errorf("%s: hook is not allowed for %s fixtures", ref, p.Manifest.Kind)
		}
		return nil
	}
	if fixture.Hook == "" {
		if p.Manifest.Kind == "" && !p.Manifest.DeclaresSiteType() {
			return nil
		}
		return fmt.Errorf("%s: hook is required", ref)
	}
	if p.Manifest.DeclaresGatewayPipeline() && fixture.Hook == HookGatewayPipelineFixture {
		return nil
	}
	if p.Manifest.DeclaresSiteType() {
		if !isValidSiteProviderFixtureHook(fixture.Hook) {
			return fmt.Errorf("%s: unknown hook %q", ref, fixture.Hook)
		}
	} else if !contains(allKnownOptionalHooks, fixture.Hook) {
		return fmt.Errorf("%s: unknown hook %q", ref, fixture.Hook)
	}
	if !p.HasHook(fixture.Hook) {
		return fmt.Errorf("%s: hook %q is not exported", ref, fixture.Hook)
	}
	return nil
}

func (p *Plugin) runHookFixture(ctx context.Context, fixture Fixture) error {
	switch fixture.Hook {
	case "probe":
		return p.runProbeFixture(ctx, fixture)
	case "listModels":
		return p.runSteppedFixtureHook(ctx, fixture, "listModels", KindModelList)
	case "check":
		return p.runSteppedFixtureHook(ctx, fixture, "check", KindCredentialCheck)
	case "detect":
		return p.runSteppedFixtureHook(ctx, fixture, "detect", KindSiteDetect)
	case "startLogin":
		return p.runAccountFixtureHook(ctx, fixture, "startLogin", KindAccountLogin)
	case "pollLogin":
		return p.runAccountFixtureHook(ctx, fixture, "pollLogin", KindAccountLogin)
	case "refreshTokens":
		return p.runAccountFixtureHook(ctx, fixture, "refreshTokens", KindAccountRefresh)
	case "summarizeAccount":
		return p.runAccountFixtureHook(ctx, fixture, "summarizeAccount", KindAccountSummary)
	case "classify", "classifyError":
		return p.runClassifyFixtureHook(ctx, fixture, fixture.Hook)
	case "rewritePayload":
		return p.runRewritePayloadFixture(ctx, fixture)
	case "describeModels":
		return p.runModelMetadataFixtureHook(ctx, fixture, "describeModels")
	case "parsePricing":
		return p.runPricingParseFixtureHook(ctx, fixture, "parsePricing")
	case "handle", "onEvent":
		return p.runEventFixtureHook(ctx, fixture, fixture.Hook)
	case "onSchedule":
		return p.runScheduleFixtureHook(ctx, fixture, "onSchedule")
	case HookSignRequest:
		return p.runStandaloneSignFixture(ctx, fixture)
	case HookTransformStream:
		return p.runTransformStreamFixture(ctx, fixture)
	case HookCreateStreamFilter:
		return p.runCreateStreamFilterFixture(ctx, fixture)
	case HookParseError:
		return p.runStandaloneParseErrorFixture(ctx, fixture)
	case HookInspectSegments:
		return p.runInspectSegmentsFixture(ctx, fixture)
	case HookGatewayPipelineFixture:
		if gatewayPipelineFixtureRunner == nil {
			return fmt.Errorf("%s: gatewayPipeline fixture runner is not registered", fixtureRef(fixture))
		}
		return gatewayPipelineFixtureRunner(ctx, p, fixture)
	case "decodeRequest", "buildRequest", "parseResponse":
		return p.runProtocolFixture(ctx, fixture)
	default:
		return fmt.Errorf("%s: unknown hook %q", fixtureRef(fixture), fixture.Hook)
	}
}

func (p *Plugin) runClassifyFixtureHook(ctx context.Context, fixture Fixture, hook string) error {
	var in ErrorClassifyInput
	if err := decodeFixtureInput(fixture.Input, &in); err != nil {
		return err
	}
	result, err := p.CallErrorClassifyHook(ctx, hook, ErrorClassifyContext{SiteType: fixture.Ctx.SiteType}, in)
	return compareOneShot(result, err, fixture)
}

func (p *Plugin) runModelMetadataFixtureHook(ctx context.Context, fixture Fixture, hook string) error {
	var in ModelMetadataInput
	if err := decodeFixtureInput(fixture.Input, &in); err != nil {
		return err
	}
	var out ModelMetadataResult
	err := p.callOneShotHook(ctx, hook, KindModelMetadata, &out, encodeValue(ModelMetadataContext{SiteType: fixture.Ctx.SiteType}), encodeValue(in))
	if err == nil {
		asked := make(map[string]bool, len(in.Models))
		for _, name := range in.Models {
			asked[name] = true
		}
		for i, entry := range out.Models {
			if !asked[entry.ID] {
				err = annotate(p, shapeError("result.models[%d].id: %q was not in the input", i, entry.ID), hook, 0, 0)
				break
			}
		}
	}
	return compareOneShot(out, err, fixture)
}

func (p *Plugin) runPricingParseFixtureHook(ctx context.Context, fixture Fixture, hook string) error {
	if fixture.Input == nil {
		return fmt.Errorf("input is required")
	}
	var out PricingParseResult
	err := p.callOneShotHook(ctx, hook, KindPricingParse, &out, encodeValue(PricingParseContext{SiteType: fixture.Ctx.SiteType}), fixture.Input)
	return compareOneShot(out, err, fixture)
}

func (p *Plugin) runEventFixtureHook(ctx context.Context, fixture Fixture, hook string) error {
	var in AutomationEvent
	if err := decodeFixtureInput(fixture.Input, &in); err != nil {
		return err
	}
	eventType := fixture.Ctx.Event
	if eventType == "" {
		eventType = in.Type
	}
	bindingID := fixture.Ctx.BindingID
	if bindingID == "" {
		bindingID = "fixture"
	}
	inputs := fixture.Ctx.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	state := fixture.Ctx.State
	if state == nil {
		state = map[string]any{}
	}
	if in.Current == nil {
		in.Current = map[string]any{}
	}
	if in.Subject.Fields == nil {
		in.Subject.Fields = map[string]any{}
	}
	c := AutomationContext{
		Event:     eventType,
		Now:       fixture.Ctx.Now,
		BindingID: bindingID,
		Inputs:    inputs,
		State:     state,
	}
	var out AutomationResult
	err := p.callOneShotHook(ctx, hook, KindAutomation, &out, encodeValue(c), encodeValue(in))
	if err == nil && len(p.Manifest.Automation.Permissions) > 0 {
		if vErr := validateAutomationResult(&out, p.Manifest.Automation.Permissions, entitiesIn(c.Inputs), p.Manifest.Automation.Inputs, c.Inputs); vErr != nil {
			err = annotate(p, vErr, hook, 0, 0)
		}
	}
	return compareOneShot(out, err, fixture)
}

func decodeStreamTransformEvent(raw any, path string) (StreamTransformEvent, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return StreamTransformEvent{}, shapeError("%s: expected object", path)
	}
	for key := range obj {
		switch key {
		case "data", "event":
		default:
			return StreamTransformEvent{}, shapeError("%s: unknown field %q", path, key)
		}
	}
	dataPath := joinPath(path, "data")
	rawData, hasData := obj["data"]
	if !hasData || rawData == nil {
		return StreamTransformEvent{}, shapeError("%s: required", dataPath)
	}
	data, ok := rawData.(string)
	if !ok {
		return StreamTransformEvent{}, shapeError("%s: expected string", dataPath)
	}
	if len(data) > MaxStreamEventBytes {
		return StreamTransformEvent{}, shapeError("%s: longer than %d bytes", dataPath, MaxStreamEventBytes)
	}
	var eventName string
	if rawEvent, hasEvent := obj["event"]; hasEvent && rawEvent != nil {
		eventPath := joinPath(path, "event")
		ev, ok := rawEvent.(string)
		if !ok {
			return StreamTransformEvent{}, shapeError("%s: expected string", eventPath)
		}
		if len(ev) > maxFieldBytes {
			return StreamTransformEvent{}, shapeError("%s: longer than %d bytes", eventPath, maxFieldBytes)
		}
		if strings.ContainsAny(ev, "\r\n") {
			return StreamTransformEvent{}, shapeError("%s: must not contain CR or LF", eventPath)
		}
		eventName = ev
	}
	return StreamTransformEvent{Event: eventName, Data: data}, nil
}

func (p *Plugin) runTransformStreamFixture(ctx context.Context, fixture Fixture) error {
	if fixture.Input == nil {
		return fmt.Errorf("input is required")
	}
	in, err := decodeStreamTransformEvent(fixture.Input, "input")
	if err != nil {
		return err
	}
	res, _, _, err := p.CallTransformStream(ctx, StreamTransformContext{
		SiteType: fixture.Ctx.SiteType,
		Model:    fixture.Ctx.Model,
		Protocol: fixture.Ctx.Protocol,
	}, in)
	return compareOneShot(res, err, fixture)
}

type streamFilterFixtureInput struct {
	Events []StreamTransformEvent
	Flush  *bool
}

type streamFilterFixtureResult struct {
	Events []StreamTransformResult `ts:"events"`
	Flush  *StreamTransformResult  `ts:"flush,optional"`
}

func decodeStreamFilterFixtureInput(input any) (streamFilterFixtureInput, error) {
	if input == nil {
		return streamFilterFixtureInput{}, fmt.Errorf("input is required")
	}
	if arr, ok := input.([]any); ok {
		input = map[string]any{"events": arr}
	}
	obj, ok := input.(map[string]any)
	if !ok {
		return streamFilterFixtureInput{}, shapeError("input: expected object")
	}
	for key := range obj {
		switch key {
		case "events", "flush":
		default:
			return streamFilterFixtureInput{}, shapeError("input: unknown field %q", key)
		}
	}
	rawEvents, hasEvents := obj["events"]
	if !hasEvents || rawEvents == nil {
		return streamFilterFixtureInput{}, shapeError("input.events: required")
	}
	items, ok := rawEvents.([]any)
	if !ok {
		return streamFilterFixtureInput{}, shapeError("input.events: expected array")
	}
	if len(items) > defaultMaxItems {
		return streamFilterFixtureInput{}, shapeError("input.events: more than %d items", defaultMaxItems)
	}
	events := make([]StreamTransformEvent, len(items))
	for i, item := range items {
		if item == nil {
			return streamFilterFixtureInput{}, shapeError("input.events[%d]: must not be null", i)
		}
		ev, err := decodeStreamTransformEvent(item, fmt.Sprintf("input.events[%d]", i))
		if err != nil {
			return streamFilterFixtureInput{}, err
		}
		events[i] = ev
	}
	in := streamFilterFixtureInput{Events: events}
	if rawFlush, hasFlush := obj["flush"]; hasFlush && rawFlush != nil {
		flag, ok := rawFlush.(bool)
		if !ok {
			return streamFilterFixtureInput{}, shapeError("input.flush: expected boolean")
		}
		in.Flush = &flag
	}
	return in, nil
}

func (p *Plugin) runCreateStreamFilterFixture(ctx context.Context, fixture Fixture) error {
	in, err := decodeStreamFilterFixtureInput(fixture.Input)
	if err != nil {
		return err
	}
	sess, err := p.OpenStreamFilter(ctx, StreamTransformContext{
		SiteType: fixture.Ctx.SiteType,
		Model:    fixture.Ctx.Model,
		Protocol: fixture.Ctx.Protocol,
	})
	if err != nil {
		return compareOneShot(nil, err, fixture)
	}
	defer sess.Close()

	events := make([]StreamTransformResult, 0, len(in.Events))
	stopped := false
	for _, ev := range in.Events {
		res, _, _, err := sess.OnEvent(ctx, ev)
		if err != nil {
			return compareOneShot(nil, err, fixture)
		}
		events = append(events, res)
		if res.Done {
			stopped = true
			break
		}
	}
	out := streamFilterFixtureResult{Events: events}
	shouldFlush := (!stopped && (in.Flush == nil || *in.Flush)) || (stopped && in.Flush != nil && *in.Flush)
	if shouldFlush && sess.HasFlush() {
		flushRes, _, _, err := sess.OnFlush(ctx)
		if err != nil {
			return compareOneShot(nil, err, fixture)
		}
		out.Flush = &flushRes
	}
	return compareOneShot(out, nil, fixture)
}

func (p *Plugin) runScheduleFixtureHook(ctx context.Context, fixture Fixture, hook string) error {
	ctxMap := map[string]any{
		"siteType": fixture.Ctx.SiteType,
		"baseURL":  fixture.Ctx.BaseURL,
		"now":      fixture.Ctx.Now,
	}
	if len(fixture.Ctx.Fields) > 0 {
		ctxMap["fields"] = fixture.Ctx.Fields
	}
	if len(fixture.Ctx.Account) > 0 {
		ctxMap["account"] = fixture.Ctx.Account
	}
	var steps []ProbeStep
	var requests []ProbeRequest
	used := make([]bool, len(fixture.Responses))
	for step := 0; step < MaxProbeSteps; step++ {
		out, _, _, err := p.callHook(ctx, hook, scheduleHookTimeout, len(steps), ctxMap, stepsAsValue(steps))
		if err != nil {
			if fixture.Expect.Error != "" && strings.Contains(err.Error(), fixture.Expect.Error) {
				return nil
			}
			return err
		}
		object, ok := out.(map[string]any)
		if !ok {
			return fmt.Errorf("onSchedule return value: expected object")
		}
		if errText, ok := object["error"].(string); ok && errText != "" {
			if errText != fixture.Expect.Error {
				return fmt.Errorf("error %q, want %q", errText, fixture.Expect.Error)
			}
			return nil
		}
		if rawReq, hasReq := object["request"]; hasReq && rawReq != nil {
			req, err := decodeRequest(rawReq)
			if err != nil {
				return err
			}
			requests = append(requests, req)
			resp, ok := takeResponse(&req, fixture.Responses, used)
			if !ok {
				return fmt.Errorf("unexpected request %s %s", req.Method, req.Path)
			}
			steps = append(steps, ProbeStep{Request: req, Response: resp})
			continue
		}
		if fixture.Expect.Error != "" {
			return fmt.Errorf("got a result, want error %q", fixture.Expect.Error)
		}
		if len(fixture.Expect.Requests) > 0 {
			if err := matchRequests(requests, fixture.Expect.Requests); err != nil {
				return err
			}
		}
		target := object
		if inner, ok := object["result"].(map[string]any); ok {
			target = inner
		}
		if fixture.Expect.Result != nil {
			return diffValue("result", target, fixture.Expect.Result)
		}
		return nil
	}
	return fmt.Errorf("%s exceeded %d steps", hook, MaxProbeSteps)
}

func (p *Plugin) runAccountFixtureHook(ctx context.Context, fixture Fixture, hook, resultKind string) error {
	var ctxValue map[string]any
	switch resultKind {
	case KindAccountLogin:
		params := fixture.Ctx.Params
		if params == nil {
			params = map[string]string{}
		}
		ctxValue = encodeValue(AccountLoginContext{
			SiteType: fixture.Ctx.SiteType,
			BaseURL:  fixture.Ctx.BaseURL,
			Params:   params,
			Now:      fixture.Ctx.Now,
		}).(map[string]any)
	default:
		fields := fixture.Ctx.Fields
		if fields == nil {
			fields = map[string]string{}
		}
		ctxValue = encodeValue(AccountCredentialContext{
			SiteType: fixture.Ctx.SiteType,
			BaseURL:  fixture.Ctx.BaseURL,
			Fields:   fields,
			Now:      fixture.Ctx.Now,
		}).(map[string]any)
	}
	var steps []ProbeStep
	var requests []ProbeRequest
	used := make([]bool, len(fixture.Responses))
	for step := 0; step < MaxProbeSteps; step++ {
		decision, err := p.CallAccountHook(ctx, hook, resultKind, ctxValue, steps)
		if err != nil {
			if fixture.Expect.Error != "" && strings.Contains(err.Error(), fixture.Expect.Error) {
				return nil
			}
			return err
		}
		if decision.Error != "" {
			if decision.Error != fixture.Expect.Error {
				return fmt.Errorf("error %q, want %q", decision.Error, fixture.Expect.Error)
			}
			return nil
		}
		if decision.Result != nil {
			if fixture.Expect.Error != "" {
				return fmt.Errorf("got a result, want error %q", fixture.Expect.Error)
			}
			if err := matchRequests(requests, fixture.Expect.Requests); err != nil {
				return err
			}
			if fixture.Expect.Result == nil {
				return nil
			}
			return diffValue("result", encodeValue(decision.Result), fixture.Expect.Result)
		}
		if decision.Request == nil {
			return fmt.Errorf("hook returned an empty decision")
		}
		requests = append(requests, *decision.Request)
		response, ok := takeResponse(decision.Request, fixture.Responses, used)
		if !ok {
			return fmt.Errorf("unexpected request %s %s", decision.Request.Method, decision.Request.Path)
		}
		steps = append(steps, ProbeStep{Request: *decision.Request, Response: response})
	}
	return fmt.Errorf("%s exceeded %d steps", hook, MaxProbeSteps)
}

func (p *Plugin) runStandaloneSignFixture(ctx context.Context, fixture Fixture) error {
	var in ProtocolSignInput
	if err := decodeFixtureInput(fixture.Input, &in); err != nil {
		return err
	}
	buildCtx := protocolBuildContextFromFixture(fixture)
	signed, _, _, err := p.CallSignRequest(ctx, buildCtx, in)
	if err != nil {
		if fixture.Expect.Error != "" && strings.Contains(err.Error(), fixture.Expect.Error) {
			return nil
		}
		return err
	}
	if fixture.Expect.Error != "" {
		return fmt.Errorf("got a result, want error %q", fixture.Expect.Error)
	}
	want := fixture.Expect.Sign
	if want == nil {
		want = fixture.Expect.Result
	}
	if want == nil {
		return nil
	}
	return matchProtocolSign(signed, want)
}

func (p *Plugin) runStandaloneParseErrorFixture(ctx context.Context, fixture Fixture) error {
	body := fixture.Response.Body
	if body == "" && fixture.Response.JSON != nil {
		encoded, err := json.Marshal(fixture.Response.JSON)
		if err != nil {
			return err
		}
		body = string(encoded)
	}
	in := ProtocolErrorInput{
		Status:  fixture.Response.Status,
		Body:    body,
		JSON:    fixture.Response.JSON,
		Headers: map[string]string{},
	}
	buildCtx := protocolBuildContextFromFixture(fixture)
	return p.runParseErrorFixture(ctx, buildCtx, in, fixture)
}

func (p *Plugin) runRewritePayloadFixture(ctx context.Context, fixture Fixture) error {
	payload, err := rewriteFixturePayload(fixture.Input)
	if err != nil {
		return err
	}
	result, err := p.CallRewritePayload(ctx, "rewritePayload", RewritePayloadContext{
		SiteType: fixture.Ctx.SiteType,
		Model:    fixture.Ctx.Model,
		Stream:   fixture.Ctx.Stream,
		Protocol: fixture.Ctx.Protocol,
	}, payload)
	return compareOneShot(result, err, fixture)
}

func rewriteFixturePayload(input any) (map[string]any, error) {
	if input == nil {
		return nil, fmt.Errorf("input is required")
	}
	object, ok := input.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input must be an object")
	}
	return object, nil
}

func (p *Plugin) runProbeFixture(ctx context.Context, fixture Fixture) error {
	probeCtx := ProbeContext{
		SiteType:       fixture.Ctx.SiteType,
		BaseURL:        fixture.Ctx.BaseURL,
		CredentialType: fixture.Ctx.CredentialType,
		Now:            fixture.Ctx.Now,
		Account:        fixture.Ctx.Account,
	}
	var steps []ProbeStep
	var requests []ProbeRequest
	used := make([]bool, len(fixture.Responses))
	for step := 0; step < MaxProbeSteps; step++ {
		decision, err := p.CallProbe(ctx, probeCtx, steps)
		if err != nil {
			return err
		}
		if decision.Error != "" {
			if decision.Error != fixture.Expect.Error {
				return fmt.Errorf("error %q, want %q", decision.Error, fixture.Expect.Error)
			}
			return nil
		}
		if decision.Result != nil {
			if fixture.Expect.Error != "" {
				return fmt.Errorf("got a result, want error %q", fixture.Expect.Error)
			}
			if err := matchRequests(requests, fixture.Expect.Requests); err != nil {
				return err
			}
			return matchResult(decision.Result, fixture.Expect.Result)
		}
		if decision.Request == nil {
			return fmt.Errorf("hook returned an empty decision")
		}
		requests = append(requests, *decision.Request)
		response, ok := takeResponse(decision.Request, fixture.Responses, used)
		if !ok {
			return fmt.Errorf("unexpected request %s %s", decision.Request.Method, decision.Request.Path)
		}
		steps = append(steps, ProbeStep{Request: *decision.Request, Response: response})
	}
	return fmt.Errorf("probe exceeded %d steps", MaxProbeSteps)
}

func takeResponse(request *ProbeRequest, responses []FixtureResponse, used []bool) (ProbeResponse, bool) {
	for i, response := range responses {
		if used[i] || !response.Match.matches(request) {
			continue
		}
		used[i] = true
		body := response.Body
		payload := response.JSON
		if body == "" && payload != nil {
			encoded, err := json.Marshal(payload)
			if err == nil {
				body = string(encoded)
			}
		}
		if payload == nil && body != "" {
			_ = json.Unmarshal([]byte(body), &payload)
		}
		return ProbeResponse{
			Status:  response.Status,
			Body:    body,
			JSON:    payload,
			Error:   response.Error,
			Headers: map[string]string{},
		}, true
	}
	return ProbeResponse{}, false
}

func (m FixtureMatch) matches(request *ProbeRequest) bool {
	if m.Method != "" && !strings.EqualFold(m.Method, request.Method) {
		return false
	}
	if m.Path != "" && request.Path != m.Path {
		return false
	}
	if m.PathPrefix != "" && !strings.HasPrefix(request.Path, m.PathPrefix) {
		return false
	}
	if m.Endpoint != "" && request.Endpoint != m.Endpoint {
		return false
	}
	if m.Sign != nil && request.Sign != *m.Sign {
		return false
	}
	return m.Path != "" || m.PathPrefix != ""
}

func matchRequests(got []ProbeRequest, want []FixtureMatch) error {
	if len(got) != len(want) {
		return fmt.Errorf("request count %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !want[i].matches(&got[i]) {
			return fmt.Errorf("request %d was %s %s", i, got[i].Method, got[i].Path)
		}
	}
	return nil
}

func matchResult(got *ProbeResult, want map[string]any) error {
	encoded, err := json.Marshal(resultMap(got))
	if err != nil {
		return err
	}
	var actual map[string]any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		return err
	}
	return diffValue("result", actual, want)
}

func matchFixtureStorage(mem *MemoryStorage, want map[string]any) error {
	actual, err := mem.Snapshot()
	if err != nil {
		return err
	}
	for key, expected := range want {
		gotVal, exists := actual[key]
		if expected == nil {
			if exists && gotVal != nil {
				return fmt.Errorf("storage.%s = %#v, want absent/null", key, gotVal)
			}
			continue
		}
		if !exists {
			return fmt.Errorf("storage.%s is missing, want %#v", key, expected)
		}
		if err := diffValue("storage."+key, gotVal, expected); err != nil {
			return err
		}
	}
	return nil
}

func resultMap(result *ProbeResult) map[string]any {
	entries := make([]any, len(result.Entries))
	for i, entry := range result.Entries {
		item := map[string]any{"label": entry.Label}
		if entry.Kind != "" {
			item["kind"] = entry.Kind
		}
		if entry.Name != "" {
			item["name"] = entry.Name
		}
		if entry.Unit != "" {
			item["unit"] = entry.Unit
		}
		if entry.Remaining != nil {
			item["remaining"] = *entry.Remaining
		}
		if entry.Limit != nil {
			item["limit"] = *entry.Limit
		}
		if entry.Used != nil {
			item["used"] = *entry.Used
		}
		if entry.Unlimited {
			item["unlimited"] = true
		}
		if entry.ResetAt != "" {
			item["resetAt"] = entry.ResetAt
		}
		if entry.ExpiresAt != "" {
			item["expiresAt"] = entry.ExpiresAt
		}
		if entry.CashBalance != nil {
			item["cashBalance"] = *entry.CashBalance
		}
		if entry.VoucherBalance != nil {
			item["voucherBalance"] = *entry.VoucherBalance
		}
		if entry.GrantedBalance != nil {
			item["grantedBalance"] = *entry.GrantedBalance
		}
		if entry.ToppedUpBalance != nil {
			item["toppedUpBalance"] = *entry.ToppedUpBalance
		}
		entries[i] = item
	}
	out := map[string]any{"kind": result.Kind, "entries": entries}
	if result.Plan != "" {
		out["plan"] = result.Plan
	}
	if result.ExpiresAt != "" {
		out["expiresAt"] = result.ExpiresAt
	}
	if result.IsAvailable != nil {
		out["isAvailable"] = *result.IsAvailable
	}
	return out
}

func diffValue(path string, got, want any) error {
	switch expected := want.(type) {
	case float64:
		number, ok := asFloat(got)
		if !ok || math.Abs(number-expected) > 1e-9 {
			return fmt.Errorf("%s = %#v, want %#v", path, got, want)
		}
		return nil
	case string:
		if got != want {
			return fmt.Errorf("%s = %#v, want %#v", path, got, want)
		}
		return nil
	case bool:
		if got != want {
			return fmt.Errorf("%s = %#v, want %#v", path, got, want)
		}
		return nil
	case []any:
		items, ok := got.([]any)
		if !ok || len(items) != len(expected) {
			return fmt.Errorf("%s = %#v, want %#v", path, got, want)
		}
		for i := range expected {
			if err := diffValue(fmt.Sprintf("%s[%d]", path, i), items[i], expected[i]); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		object, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s = %#v, want object", path, got)
		}
		for key, value := range expected {
			if err := diffValue(path+"."+key, object[key], value); err != nil {
				return err
			}
		}
		return nil
	default:
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("%s = %#v, want %#v", path, got, want)
		}
		return nil
	}
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}
