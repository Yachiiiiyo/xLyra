package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
)

// SubmitPackage forwards a signed .xlp to POST /api/instance/submissions.
func (c *Client) SubmitPackage(ctx context.Context, pluginID, version string, pkg []byte, changelog string) (Submission, error) {
	pluginID = strings.TrimSpace(pluginID)
	version = strings.TrimSpace(version)
	changelog = strings.TrimSpace(changelog)
	if pluginID == "" || version == "" {
		return Submission{}, errors.New("pluginId 与 version 必填")
	}
	row, err := c.boundRow(ctx)
	if err != nil {
		return Submission{}, err
	}
	body, contentType, err := submissionMultipart(pkg, changelog)
	if err != nil {
		return Submission{}, err
	}
	status, raw, err := c.signedExchange(ctx, row, http.MethodPost, "/api/instance/submissions", "", contentType, body)
	if err != nil {
		return Submission{}, err
	}
	if status < 200 || status >= 300 {
		return Submission{}, hubError(status, raw)
	}
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || strings.TrimSpace(resp.ID) == "" {
		return Submission{}, errors.New("Hub 返回的提交响应无效")
	}
	item := Submission{
		ID:       strings.TrimSpace(resp.ID),
		PluginID: pluginID,
		Version:  version,
		Status:   strings.TrimSpace(resp.Status),
	}
	c.rememberSubmission(item)
	return item, nil
}

func submissionMultipart(pkg []byte, changelog string) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("package", "plugin.xlp")
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(pkg); err != nil {
		return nil, "", err
	}
	if err := writer.WriteField("changelog", changelog); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}
