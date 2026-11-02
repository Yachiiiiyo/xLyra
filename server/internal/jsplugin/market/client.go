package market

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"xlyra/server/internal/version"
)

func userAgent() string {
	return "xLyra/" + version.Current().Version
}

func fetch(ctx context.Context, client *http.Client, rawURL, etag string, maxBytes int) (body []byte, newETag string, notModified bool, err error) {
	if client == nil {
		return nil, "", false, fmt.Errorf("missing http client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", userAgent())
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, resp.Header.Get("ETag"), true, nil
	case http.StatusOK:
		body, err = readLimited(resp.Body, maxBytes)
		if err != nil {
			return nil, "", false, err
		}
		return body, resp.Header.Get("ETag"), false, nil
	default:
		return nil, "", false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func readLimited(r io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid size limit")
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	return body, nil
}
