package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultCloudURL = "https://xlyra.yachiyo.im"

type publishOptions struct {
	packagePath   string
	changelog     string
	changelogFile string
	cloud         string
	token         string
	noWait        bool
	timeout       time.Duration
	pollInterval  time.Duration
	client        *http.Client
	out           io.Writer
}

func runPublish(args []string) {
	opts, err := parsePublishArgs(args, os.Getenv("XLYRA_CLOUD_TOKEN"))
	if err != nil {
		fatal(err)
	}
	opts.out = os.Stdout
	if err := publishPackage(opts); err != nil {
		fatal(err)
	}
}

func parsePublishArgs(args []string, envToken string) (publishOptions, error) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	changelog := fs.String("changelog", "", "更新日志")
	changelogFile := fs.String("changelog-file", "", "从文件读取更新日志")
	cloud := fs.String("cloud", defaultCloudURL, "xLyra Cloud 地址，必须是 https")
	token := fs.String("token", "", "xLyra Cloud 令牌。会进入 shell 历史，请改用环境变量 XLYRA_CLOUD_TOKEN")
	noWait := fs.Bool("no-wait", false, "提交后不等待审核结果")
	if err := fs.Parse(args); err != nil {
		return publishOptions{}, fmt.Errorf("参数无效")
	}
	if fs.NArg() != 1 {
		return publishOptions{}, fmt.Errorf("需要插件包路径")
	}
	switch {
	case *changelog == "" && *changelogFile == "":
		return publishOptions{}, fmt.Errorf("需要填写更新日志")
	case *changelog != "" && *changelogFile != "":
		return publishOptions{}, fmt.Errorf("更新日志请只填写一处")
	}
	got := strings.TrimSpace(*token)
	if got == "" {
		got = strings.TrimSpace(envToken)
	}
	if got == "" {
		return publishOptions{}, fmt.Errorf("需要 xLyra Cloud 令牌，请设置环境变量 XLYRA_CLOUD_TOKEN")
	}
	base, err := normalizeCloud(*cloud)
	if err != nil {
		return publishOptions{}, err
	}
	return publishOptions{
		packagePath:   fs.Arg(0),
		changelog:     *changelog,
		changelogFile: *changelogFile,
		cloud:         base,
		token:         got,
		noWait:        *noWait,
		timeout:       10 * time.Minute,
		pollInterval:  3 * time.Second,
	}, nil
}

func normalizeCloud(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme == "" || u.User != nil {
		return "", fmt.Errorf("xLyra Cloud 地址无效")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("xLyra Cloud 地址无效")
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
	case "http":
		if host != "127.0.0.1" && host != "localhost" {
			return "", fmt.Errorf("xLyra Cloud 地址必须是 https")
		}
	default:
		return "", fmt.Errorf("xLyra Cloud 地址必须是 https")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func publishPackage(opts publishOptions) error {
	if opts.out == nil {
		opts.out = os.Stdout
	}
	if opts.timeout <= 0 {
		opts.timeout = 10 * time.Minute
	}
	if opts.pollInterval <= 0 {
		opts.pollInterval = 3 * time.Second
	}
	text, err := loadChangelog(opts)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(opts.packagePath)
	if err != nil {
		return fmt.Errorf("无法读取插件包")
	}
	pkg, _, err := inspectPackage(raw)
	if err != nil {
		return fmt.Errorf("本地校验未通过: %s", scrub(err.Error(), opts.token))
	}
	if pkg.Signer == "" {
		return fmt.Errorf("包未签名，请先签名后再发布")
	}
	client := opts.client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	id, status, err := postSubmission(ctx, client, opts.cloud, opts.token, raw, text)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "已提交 %s@%s\n", pkg.Manifest.ID, pkg.Manifest.Version)
	fmt.Fprintf(opts.out, "提交编号 %s\n", id)
	fmt.Fprintf(opts.out, "状态：%s\n", statusLabel(status))
	if opts.noWait {
		return nil
	}
	if terminalStatus(status) {
		return finishStatus(opts.out, status, "")
	}
	last := status
	for {
		view, err := getSubmission(ctx, client, opts.cloud, opts.token, id)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("等待超时，当前状态：%s", statusLabel(last))
			}
			return err
		}
		if view.Status != last {
			fmt.Fprintf(opts.out, "状态：%s\n", statusLabel(view.Status))
			last = view.Status
		}
		if terminalStatus(view.Status) {
			reason := view.ReviewNote
			if view.Status == "check_failed" {
				reason = view.CheckSummary
			}
			return finishStatus(opts.out, view.Status, reason)
		}
		timer := time.NewTimer(opts.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("等待超时，当前状态：%s", statusLabel(last))
		case <-timer.C:
		}
	}
}

func loadChangelog(opts publishOptions) (string, error) {
	text := opts.changelog
	if opts.changelogFile != "" {
		raw, err := os.ReadFile(opts.changelogFile)
		if err != nil {
			return "", fmt.Errorf("无法读取更新日志")
		}
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 2000 {
		return "", fmt.Errorf("需要填写 1–2000 字的更新日志")
	}
	return text, nil
}

type cloudSubmission struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CheckSummary string `json:"checkSummary"`
	ReviewNote   string `json:"reviewNote"`
}

func postSubmission(ctx context.Context, client *http.Client, cloud, token string, pkg []byte, changelog string) (string, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("package", "plugin.xlp")
	if err != nil {
		return "", "", fmt.Errorf("无法准备上传")
	}
	if _, err := part.Write(pkg); err != nil {
		return "", "", fmt.Errorf("无法准备上传")
	}
	if err := w.WriteField("changelog", changelog); err != nil {
		return "", "", fmt.Errorf("无法准备上传")
	}
	if err := w.Close(); err != nil {
		return "", "", fmt.Errorf("无法准备上传")
	}
	view, err := cloudDo(ctx, client, http.MethodPost, cloud+"/api/dev/token/submissions", token, buf.Bytes(), w.FormDataContentType())
	if err != nil {
		return "", "", err
	}
	if view.ID == "" {
		return "", "", fmt.Errorf("提交响应无效")
	}
	return view.ID, view.Status, nil
}

func getSubmission(ctx context.Context, client *http.Client, cloud, token, id string) (cloudSubmission, error) {
	return cloudDo(ctx, client, http.MethodGet, cloud+"/api/dev/token/submissions/"+url.PathEscape(id), token, nil, "")
}

func cloudDo(ctx context.Context, client *http.Client, method, endpoint, token string, body []byte, contentType string) (cloudSubmission, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, reader)
	if err != nil {
		return cloudSubmission{}, fmt.Errorf("xLyra Cloud 地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return cloudSubmission{}, fmt.Errorf("连接 xLyra Cloud 失败")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return cloudSubmission{}, fmt.Errorf("读取 xLyra Cloud 响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cloudSubmission{}, fmt.Errorf("%s", cloudFailure(raw, token))
	}
	var view cloudSubmission
	if json.Unmarshal(raw, &view) != nil {
		return cloudSubmission{}, fmt.Errorf("xLyra Cloud 响应无效")
	}
	return view, nil
}

func cloudFailure(raw []byte, token string) string {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	if text, ok := cloudErrorText[body.Error.Code]; ok {
		return text
	}
	message := scrub(body.Error.Message, token)
	if message != "" {
		return message
	}
	if body.Error.Code != "" {
		return "提交失败（" + body.Error.Code + "）"
	}
	return "提交失败"
}

func scrub(text, token string) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, "")
	}
	return strings.ReplaceAll(text, "xlh_", "")
}

var cloudErrorText = map[string]string{
	"token_invalid":         "令牌无效",
	"token_expired":         "令牌已过期",
	"token_revoked":         "令牌已撤销",
	"not_developer":         "不是有效开发者",
	"insufficient_scope":    "令牌没有发布权限",
	"no_active_key":         "没有可用的签名公钥",
	"namespace_not_active":  "命名空间未激活",
	"signer_not_registered": "签名者不在已登记的有效公钥中",
	"version_not_greater":   "版本号必须大于已有版本",
	"too_many_pending":      "未结案的提交过多",
	"daily_limit":           "今日提交次数已达上限",
	"changelog_required":    "需要填写 1–2000 字的更新日志",
	"rate_limited":          "请求过于频繁",
	"payload_too_large":     "包不能超过 2MB",
	"check_unavailable":     "检查服务暂不可用",
	"unauthenticated":       "未登录",
}

func statusLabel(status string) string {
	switch status {
	case "submitted":
		return "已提交"
	case "checking":
		return "检查中"
	case "check_failed":
		return "检查未通过"
	case "in_review":
		return "待审核"
	case "rejected":
		return "已退回"
	case "approved":
		return "已批准"
	case "published":
		return "已发布"
	case "withdrawn":
		return "已撤回"
	default:
		return status
	}
}

func terminalStatus(status string) bool {
	switch status {
	case "in_review", "approved", "published", "rejected", "check_failed", "withdrawn":
		return true
	default:
		return false
	}
}

func finishStatus(out io.Writer, status, reason string) error {
	switch status {
	case "in_review", "approved", "published":
		return nil
	default:
		if reason != "" {
			fmt.Fprintf(out, "原因：%s\n", reason)
		}
		if status == "check_failed" {
			return fmt.Errorf("检查未通过")
		}
		if status == "rejected" {
			return fmt.Errorf("已退回")
		}
		if status == "withdrawn" {
			return fmt.Errorf("已撤回")
		}
		return fmt.Errorf("提交未完成")
	}
}
