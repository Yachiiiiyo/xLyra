package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xlyra/server/internal/store"
)

const (
	DeveloperNone      = "none"
	DeveloperPending   = "pending"
	DeveloperActive    = "active"
	DeveloperSuspended = "suspended"
	DeveloperRejected  = "rejected"

	developerCacheTTL = 5 * time.Minute
	developerWarnGap  = time.Minute
)

// DeveloperProfile is the instance's view of the bound Hub account.
// Unavailable is set when the Hub could not be asked; Status is then none.
type DeveloperProfile struct {
	Status      string               `json:"status"`
	DisplayName string               `json:"display_name"`
	Namespaces  []DeveloperNamespace `json:"namespaces"`
	Keys        []DeveloperKey       `json:"keys"`
	Plugins     []DeveloperPlugin    `json:"plugins"`
	Unavailable bool                 `json:"unavailable"`
}

type DeveloperNamespace struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type DeveloperKey struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	Status      string `json:"status"`
}

type DeveloperPlugin struct {
	ID            string `json:"id"`
	LatestVersion string `json:"latest_version"`
	LatestStatus  string `json:"latest_status"`
}

type developerCache struct {
	profile    DeveloperProfile
	instanceID string
	at         time.Time
	have       bool
}

type hubDeveloperBody struct {
	Status      string `json:"status"`
	DisplayName string `json:"displayName"`
	Namespaces  []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"namespaces"`
	Keys []struct {
		Fingerprint string `json:"fingerprint"`
		Label       string `json:"label"`
		Status      string `json:"status"`
	} `json:"keys"`
	Plugins []struct {
		ID            string `json:"id"`
		LatestVersion string `json:"latestVersion"`
		LatestStatus  string `json:"latestStatus"`
	} `json:"plugins"`
}

func emptyDeveloper() DeveloperProfile {
	return DeveloperProfile{
		Status:     DeveloperNone,
		Namespaces: []DeveloperNamespace{},
		Keys:       []DeveloperKey{},
		Plugins:    []DeveloperPlugin{},
	}
}

// Developer returns the cached Hub developer profile. refresh skips the cache.
// Unbound, Hub down, and not-a-developer all come back as status none and a nil error.
// A non-nil error is only a local database failure.
func (c *Client) Developer(ctx context.Context, refresh bool) (DeveloperProfile, error) {
	if c == nil {
		return emptyDeveloper(), nil
	}
	row, err := c.repo.Get(ctx)
	if err != nil {
		return DeveloperProfile{}, err
	}
	if row.Status != store.HubStatusBound || row.InstanceID == "" {
		return emptyDeveloper(), nil
	}
	if !refresh {
		if profile, ok := c.loadDeveloper(row.InstanceID); ok {
			return profile, nil
		}
	}
	profile, cache := c.fetchDeveloper(ctx, row)
	if cache {
		c.storeDeveloper(row.InstanceID, profile)
	}
	return profile, nil
}

// CachedDeveloper is the in-memory profile, if it is still inside the 5 minute window.
// It never calls the Hub.
func (c *Client) CachedDeveloper(ctx context.Context) DeveloperProfile {
	if c == nil {
		return emptyDeveloper()
	}
	row, err := c.repo.Get(ctx)
	if err != nil || row.Status != store.HubStatusBound || row.InstanceID == "" {
		return emptyDeveloper()
	}
	profile, ok := c.loadDeveloper(row.InstanceID)
	if !ok {
		return emptyDeveloper()
	}
	return profile
}

func (c *Client) loadDeveloper(instanceID string) (DeveloperProfile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.developer.have || c.developer.instanceID != instanceID {
		return DeveloperProfile{}, false
	}
	if c.now().Sub(c.developer.at) >= developerCacheTTL {
		return DeveloperProfile{}, false
	}
	return c.developer.profile, true
}

func (c *Client) storeDeveloper(instanceID string, profile DeveloperProfile) {
	c.mu.Lock()
	c.developer = developerCache{profile: profile, instanceID: instanceID, at: c.now(), have: true}
	c.mu.Unlock()
}

func (c *Client) clearDeveloperCache() {
	c.mu.Lock()
	c.developer = developerCache{}
	c.submissions = nil
	c.mu.Unlock()
}

func (c *Client) fetchDeveloper(ctx context.Context, row store.HubBinding) (DeveloperProfile, bool) {
	none := emptyDeveloper()
	priv, err := c.privateKey(row)
	if err != nil {
		none.Unavailable = true
		c.noteDeveloperFailure(ctx, "实例私钥无法读取")
		return none, false
	}
	hub := row.HubURL
	if hub == "" {
		hub, err = c.hubURL(ctx)
		if err != nil {
			none.Unavailable = true
			c.noteDeveloperFailure(ctx, err.Error())
			return none, false
		}
	}
	timeout := c.http.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status, raw, err := c.exchange(callCtx, http.MethodGet, hub, "/api/instance/developer", "", nil, "", &signedRequest{
		instanceID: row.InstanceID,
		privateKey: priv,
	})
	if err != nil {
		none.Unavailable = true
		c.noteDeveloperFailure(ctx, err.Error())
		return none, false
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		hubErr, _ := hubError(status, raw).(*HubError)
		if hubErr != nil && hubErr.Code == "instance_revoked" {
			_ = c.repo.ClearBinding(ctx, store.HubStatusRevoked, revokedMessage)
			c.clearDeveloperCache()
			c.warnDeveloper("hub binding revoked")
			return none, false
		}
		if hubErr != nil && hubErr.Code == "not_developer" {
			return none, true
		}
	}
	if status < 200 || status >= 300 {
		hubErr, _ := hubError(status, raw).(*HubError)
		message := "读取开发者身份失败"
		if hubErr != nil && hubErr.Code != "" {
			message = message + ": " + hubErr.Code
		}
		none.Unavailable = true
		c.noteDeveloperFailure(ctx, message)
		return none, false
	}
	var body hubDeveloperBody
	if err := json.Unmarshal(raw, &body); err != nil {
		none.Unavailable = true
		c.noteDeveloperFailure(ctx, "Hub 返回的开发者身份无效")
		return none, false
	}
	return profileFromHub(body), true
}

func profileFromHub(body hubDeveloperBody) DeveloperProfile {
	profile := emptyDeveloper()
	status := strings.TrimSpace(body.Status)
	switch status {
	case DeveloperPending, DeveloperActive, DeveloperSuspended, DeveloperRejected:
		profile.Status = status
	default:
		profile.Status = DeveloperNone
	}
	profile.DisplayName = strings.TrimSpace(body.DisplayName)
	if len(body.Namespaces) > 0 {
		profile.Namespaces = make([]DeveloperNamespace, 0, len(body.Namespaces))
		for _, item := range body.Namespaces {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			profile.Namespaces = append(profile.Namespaces, DeveloperNamespace{Name: name, Status: strings.TrimSpace(item.Status)})
		}
	}
	if len(body.Keys) > 0 {
		profile.Keys = make([]DeveloperKey, 0, len(body.Keys))
		for _, item := range body.Keys {
			fp := strings.TrimSpace(item.Fingerprint)
			if fp == "" {
				continue
			}
			profile.Keys = append(profile.Keys, DeveloperKey{Fingerprint: fp, Label: strings.TrimSpace(item.Label), Status: strings.TrimSpace(item.Status)})
		}
	}
	if len(body.Plugins) > 0 {
		profile.Plugins = make([]DeveloperPlugin, 0, len(body.Plugins))
		for _, item := range body.Plugins {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			profile.Plugins = append(profile.Plugins, DeveloperPlugin{
				ID: id, LatestVersion: strings.TrimSpace(item.LatestVersion), LatestStatus: strings.TrimSpace(item.LatestStatus),
			})
		}
	}
	return profile
}

func (c *Client) noteDeveloperFailure(ctx context.Context, message string) {
	c.noteError(ctx, message)
	c.warnDeveloper(message)
}

func (c *Client) warnDeveloper(message string) {
	if message == "" {
		return
	}
	c.mu.Lock()
	if !c.developerWarnAt.IsZero() && c.now().Sub(c.developerWarnAt) < developerWarnGap {
		c.mu.Unlock()
		return
	}
	c.developerWarnAt = c.now()
	c.mu.Unlock()
	c.warn("hub developer identity unavailable", "error", message)
}

// OwnsPlugin reports whether pluginID belongs to an active developer:
// the id starts with an active namespace plus ".", and signer is an active key.
func OwnsPlugin(pluginID, signer string, profile DeveloperProfile) bool {
	if profile.Status != DeveloperActive || profile.Unavailable {
		return false
	}
	pluginID = strings.TrimSpace(pluginID)
	signer = strings.TrimSpace(signer)
	if pluginID == "" || signer == "" || !strings.Contains(pluginID, ".") {
		return false
	}
	namespaced := false
	for _, item := range profile.Namespaces {
		name := strings.TrimSpace(item.Name)
		if item.Status == DeveloperActive && name != "" && strings.HasPrefix(pluginID, name+".") {
			namespaced = true
			break
		}
	}
	if !namespaced {
		return false
	}
	for _, key := range profile.Keys {
		if key.Status == DeveloperActive && key.Fingerprint == signer {
			return true
		}
	}
	return false
}

// Submission is one Hub submission, in the admin API's snake_case shape.
type Submission struct {
	ID           string `json:"id"`
	PluginID     string `json:"plugin_id"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	CheckSummary string `json:"check_summary,omitempty"`
	ReviewNote   string `json:"review_note,omitempty"`
	SubmittedAt  string `json:"submitted_at,omitempty"`
}

type hubSubmissionBody struct {
	ID           string `json:"id"`
	PluginID     string `json:"pluginId"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	CheckSummary string `json:"checkSummary"`
	ReviewNote   string `json:"reviewNote"`
	SubmittedAt  string `json:"submittedAt"`
}

func (c *Client) CachedSubmission(pluginID string) (Submission, bool) {
	if c == nil {
		return Submission{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submissions == nil {
		return Submission{}, false
	}
	item, ok := c.submissions[pluginID]
	return item, ok
}

func (c *Client) rememberSubmission(item Submission) {
	if item.PluginID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submissions == nil {
		c.submissions = map[string]Submission{}
	}
	c.submissions[item.PluginID] = item
}

func (c *Client) rememberSubmissions(pluginID string, items []Submission) {
	latest, ok := latestSubmission(items)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submissions == nil {
		c.submissions = map[string]Submission{}
	}
	if !ok {
		delete(c.submissions, pluginID)
		return
	}
	c.submissions[pluginID] = latest
}

func latestSubmission(items []Submission) (Submission, bool) {
	if len(items) == 0 {
		return Submission{}, false
	}
	best := items[0]
	bestAt := best.SubmittedAt
	for _, item := range items[1:] {
		if item.SubmittedAt > bestAt {
			best = item
			bestAt = item.SubmittedAt
		}
	}
	return best, true
}

func (c *Client) signedExchange(ctx context.Context, row store.HubBinding, method, path, rawQuery, contentType string, body []byte) (int, []byte, error) {
	priv, err := c.privateKey(row)
	if err != nil {
		return 0, nil, errors.New("实例私钥无法读取")
	}
	hub := row.HubURL
	if hub == "" {
		hub, err = c.hubURL(ctx)
		if err != nil {
			return 0, nil, err
		}
	}
	timeout := c.http.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.exchange(callCtx, method, hub, path, rawQuery, body, contentType, &signedRequest{
		instanceID: row.InstanceID,
		privateKey: priv,
	})
}

// ErrNotBound is returned when a signed Hub call is made without a binding.
var ErrNotBound = errors.New("实例未连接")

func (c *Client) boundRow(ctx context.Context) (store.HubBinding, error) {
	row, err := c.repo.Get(ctx)
	if err != nil {
		return store.HubBinding{}, err
	}
	if row.Status != store.HubStatusBound || row.InstanceID == "" {
		return store.HubBinding{}, ErrNotBound
	}
	return row, nil
}

// ListSubmissions proxies GET /api/instance/submissions?pluginId= and remembers the latest row.
func (c *Client) ListSubmissions(ctx context.Context, pluginID string) ([]Submission, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, errors.New("pluginId 必填")
	}
	row, err := c.boundRow(ctx)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.signedExchange(ctx, row, http.MethodGet, "/api/instance/submissions", "pluginId="+url.QueryEscape(pluginID), "", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, hubError(status, raw)
	}
	items, err := parseSubmissions(raw)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].PluginID == "" {
			items[i].PluginID = pluginID
		}
	}
	c.rememberSubmissions(pluginID, items)
	return items, nil
}

func parseSubmissions(raw []byte) ([]Submission, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []Submission{}, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []hubSubmissionBody
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, errors.New("Hub 返回的提交列表无效")
		}
		return convertSubmissions(list), nil
	}
	var wrapped struct {
		Items []hubSubmissionBody `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, errors.New("Hub 返回的提交列表无效")
	}
	if wrapped.Items == nil {
		return []Submission{}, nil
	}
	return convertSubmissions(wrapped.Items), nil
}

func convertSubmissions(list []hubSubmissionBody) []Submission {
	out := make([]Submission, 0, len(list))
	for _, item := range list {
		out = append(out, Submission{
			ID:           strings.TrimSpace(item.ID),
			PluginID:     strings.TrimSpace(item.PluginID),
			Version:      strings.TrimSpace(item.Version),
			Status:       strings.TrimSpace(item.Status),
			CheckSummary: item.CheckSummary,
			ReviewNote:   item.ReviewNote,
			SubmittedAt:  strings.TrimSpace(item.SubmittedAt),
		})
	}
	return out
}
