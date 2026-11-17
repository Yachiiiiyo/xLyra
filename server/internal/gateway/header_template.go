package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
)

// headerTemplateDynamic resolves placeholders that are not in the static var map.
// A false ok leaves the placeholder missing, which is an error except for the
// keys RenderHeaderTemplate treats as empty.
type headerTemplateDynamic func(key string) (string, bool)

// RenderHeaderTemplate replaces placeholders using vars and, when set, dynamic.
// A missing static variable is an error. {{model.meta.*}} and {{session.key}}
// render as an empty string when dynamic does not supply them. The rendered
// text must not contain CR, LF, or any other control character.
func RenderHeaderTemplate(template string, vars map[string]string, dynamic ...headerTemplateDynamic) (string, error) {
	pieces, err := jsplugin.SplitHeaderTemplate(template)
	if err != nil {
		return "", err
	}
	var lookup headerTemplateDynamic
	if len(dynamic) > 0 {
		lookup = dynamic[0]
	}
	var b strings.Builder
	for _, piece := range pieces {
		if piece.Key == "" {
			b.WriteString(piece.Literal)
			continue
		}
		value, ok := vars[piece.Key]
		if !ok && lookup != nil {
			value, ok = lookup(piece.Key)
		}
		if !ok && (piece.Key == "session.key" || strings.HasPrefix(piece.Key, "model.meta.")) {
			value, ok = "", true
		}
		if !ok {
			return "", fmt.Errorf("missing variable %s", piece.Key)
		}
		b.WriteString(value)
	}
	rendered := b.String()
	if headerValueHasControl(rendered) {
		return "", fmt.Errorf("value contains a control character")
	}
	return rendered, nil
}

func dynamicHeaderValue(key, modelName, sessionKey string, meta map[string]any, settings map[string]any) (string, bool) {
	switch {
	case key == "uuid":
		return uuid.NewString(), true
	case key == "now.unix":
		return strconv.FormatInt(time.Now().UTC().Unix(), 10), true
	case key == "now.unix_ms":
		return strconv.FormatInt(time.Now().UTC().UnixMilli(), 10), true
	case key == "now.iso":
		return time.Now().UTC().Format(time.RFC3339), true
	case key == "model.name":
		return modelName, true
	case key == "session.key":
		return sessionKey, true
	case strings.HasPrefix(key, "random.hex:"):
		n, err := strconv.Atoi(strings.TrimPrefix(key, "random.hex:"))
		if err != nil || n < 1 || n > 128 {
			return "", false
		}
		buf := make([]byte, (n+1)/2)
		if _, err := rand.Read(buf); err != nil {
			return "", false
		}
		return hex.EncodeToString(buf)[:n], true
	case strings.HasPrefix(key, "model.meta."):
		return modelMetaString(meta, strings.TrimPrefix(key, "model.meta.")), true
	case strings.HasPrefix(key, "setting."):
		return settingString(settings, strings.TrimPrefix(key, "setting.")), true
	default:
		return "", false
	}
}

func modelMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	switch value := meta[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func settingString(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	switch value := settings[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func headerValueHasControl(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// pluginHeaderTemplateVars builds the variable map for one upstream call.
// A nil headerVars means an API-key site: cred.api_key is the decrypted key.
// A non-nil map is used as given (account-pool resolvers fill their own fields).
// site.baseURL is always the base URL this request uses, without a trailing slash.
func pluginHeaderTemplateVars(headerVars map[string]string, upstreamKey, baseURL string) map[string]string {
	vars := make(map[string]string, len(headerVars)+2)
	for key, value := range headerVars {
		vars[key] = value
	}
	if headerVars == nil {
		vars["cred.api_key"] = upstreamKey
	}
	vars["site.baseURL"] = strings.TrimRight(baseURL, "/")
	return vars
}

// applyPluginRequestHeaders renders the site type base template, then each
// mounted plugin's top-level request headers. Later plugins overwrite earlier
// headers of the same name. It runs after protocol headers and before signing.
func applyPluginRequestHeaders(req *http.Request, candidate routeengine.Candidate, request gatewayRequest, headerVars map[string]string, upstreamKey, baseURL, modelName string, meta map[string]any, sessionSecret []byte) error {
	if req == nil {
		return nil
	}
	vars := pluginHeaderTemplateVars(headerVars, upstreamKey, baseURL)
	vars["model.name"] = modelName
	item, ok := site.LookupPluginSiteType(candidate.Site.SiteType)
	if ok && (len(item.Request.Headers) > 0 || item.Request.StripDownstreamHeaders) {
		dynamic := headerDynamicFor(nil, candidate, request, modelName, meta, sessionSecret)
		if err := renderRequestHeaders(req, item.Request.Headers, item.Request.StripDownstreamHeaders, vars, dynamic); err != nil {
			return err
		}
	}
	for _, plugin := range requestChainPlugins(candidate) {
		if plugin.Manifest.DeclaresSiteType() || len(plugin.Manifest.Request.Headers) == 0 {
			continue
		}
		dynamic := headerDynamicFor(plugin, candidate, request, modelName, meta, sessionSecret)
		if err := renderRequestHeaders(req, plugin.Manifest.Request.Headers, false, vars, dynamic); err != nil {
			return err
		}
	}
	return nil
}

func headerDynamicFor(plugin *jsplugin.Plugin, candidate routeengine.Candidate, request gatewayRequest, modelName string, meta map[string]any, sessionSecret []byte) headerTemplateDynamic {
	pluginID := ""
	var settings map[string]any
	if plugin != nil {
		pluginID = plugin.Manifest.ID
		settings = plugin.EffectiveSettings(candidate.Site.PluginSettings[pluginID])
	}
	sessionKey := gatewaySessionKey(request, candidate.Site.ID, pluginID, sessionSecret)
	return func(key string) (string, bool) {
		return dynamicHeaderValue(key, modelName, sessionKey, meta, settings)
	}
}

func renderRequestHeaders(req *http.Request, headers map[string]jsplugin.HeaderSpec, strip bool, vars map[string]string, dynamic headerTemplateDynamic) error {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	rendered := make([]renderedPluginHeader, 0, len(names))
	var omitted []string
	for _, name := range names {
		spec := headers[name]
		if !headerSpecIncluded(spec, vars) {
			omitted = append(omitted, name)
			continue
		}
		value, err := RenderHeaderTemplate(spec.Value, vars, dynamic)
		if err != nil {
			return fmt.Errorf("header %s: %s", name, err.Error())
		}
		rendered = append(rendered, renderedPluginHeader{name: name, value: value})
	}
	if strip {
		stripPluginDownstreamHeaders(req, headers)
	}
	for _, name := range omitted {
		req.Header.Del(name)
	}
	for _, header := range rendered {
		req.Header.Set(header.name, header.value)
	}
	return nil
}

// headerSpecIncluded reports whether a header should be sent.
// A missing variable counts as empty. A failed condition omits the header
// instead of rendering an empty value.
func headerSpecIncluded(spec jsplugin.HeaderSpec, vars map[string]string) bool {
	if spec.When == "" && spec.Unless == "" {
		return true
	}
	if spec.When != "" {
		return strings.TrimSpace(vars[spec.When]) != ""
	}
	return strings.TrimSpace(vars[spec.Unless]) == ""
}

type renderedPluginHeader struct {
	name  string
	value string
}

func stripPluginDownstreamHeaders(req *http.Request, templates map[string]jsplugin.HeaderSpec) {
	keep := map[string]struct{}{
		"Content-Type": {},
		"Accept":       {},
	}
	for name := range templates {
		keep[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range req.Header {
		if _, ok := keep[name]; !ok {
			req.Header.Del(name)
		}
	}
}
