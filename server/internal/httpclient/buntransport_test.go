package httpclient

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBunRequestHeadLayoutPinsTheBunWireShape asserts the exact head bytes
// Bun's fetch writes: common headers first in their fixed order and spelling,
// every other header lowercased and sorted, then the headers fetch itself
// adds (Connection, Accept, Host, Accept-Encoding, Content-Length).
func TestBunRequestHeadLayoutPinsTheBunWireShape(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/inference/v1/chat/completions", strings.NewReader(`{"x":1}`))
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/latest/2.1.1/cli")
	req.Header.Set("X-Opencode-Client", "cli")
	req.Header.Set("X-Opencode-Project", "0123456789abcdef0123456789abcdef01234567")

	head, decompress, err := bunRequestHead(req, []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("bunRequestHead: %v", err)
	}
	if !decompress {
		t.Fatal("decompress = false, want the transport to own Accept-Encoding")
	}
	want := strings.Join([]string{
		"POST /inference/v1/chat/completions HTTP/1.1",
		"Authorization: Bearer public",
		"Content-Length: 7",
		"Content-Type: application/json",
		"User-Agent: opencode/latest/2.1.1/cli",
		"x-opencode-client: cli",
		"x-opencode-project: 0123456789abcdef0123456789abcdef01234567",
		"Connection: keep-alive",
		"Accept: */*",
		"Host: opencode.ai",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"",
		"",
	}, "\r\n")
	if string(head) != want {
		t.Fatalf("head =\n%q\nwant\n%q", head, want)
	}
}

// TestBunRequestHeadKeepsCallerAcceptsAndEncodings checks the caller-owned
// variations: a real Accept lands in the common block, a caller-set
// Accept-Encoding stays verbatim and disables the transport's decoding.
func TestBunRequestHeadKeepsCallerAcceptsAndEncodings(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodPost, "https://opencode.ai:443/v1/chat", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	head, decompress, err := bunRequestHead(req, nil)
	if err != nil {
		t.Fatalf("bunRequestHead: %v", err)
	}
	if decompress {
		t.Fatal("decompress = true, want caller-owned Accept-Encoding")
	}
	rendered := string(head)
	if !strings.Contains(rendered, "Accept: application/json\r\n") {
		t.Errorf("head = %q, want the caller Accept in the common block", rendered)
	}
	if !strings.Contains(rendered, "Accept-Encoding: gzip\r\n") {
		t.Errorf("head = %q, want the caller Accept-Encoding verbatim", rendered)
	}
	if strings.Contains(rendered, "Accept: */*") {
		t.Errorf("head = %q, must not add a default Accept", rendered)
	}
	if strings.Contains(rendered, "Content-Length") {
		t.Errorf("head = %q, must not add Content-Length without a body", rendered)
	}
}

// TestBunTransportWritesHeadAndReusesConnections serves a raw HTTP/1.1
// endpoint that records the first request head verbatim and counts accepted
// connections; two requests through one BunTransport must produce the Bun
// head and share a single pooled connection.
func TestBunTransportWritesHeadAndReusesConnections(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	var connections atomic.Int32
	firstHead := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		reader := bufio.NewReader(conn)
		head, err := readRawHead(reader)
		if err != nil {
			return
		}
		select {
		case firstHead <- head:
		default:
		}
		if err := serveOneBunExchange(reader, conn, head); err != nil {
			return
		}
		// Second request on the pooled connection.
		head2, err := readRawHead(reader)
		if err != nil {
			return
		}
		_ = serveOneBunExchange(reader, conn, head2)
	}()

	transport := NewBunTransport(BunOptions{ResponseHeaderTimeout: 5 * time.Second})
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	url := "http://" + listener.Addr().String() + "/v1/chat/completions"

	for _, name := range []string{"first", "second"} {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"n":1}`))
		req.Header.Set("Authorization", "Bearer public")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "opencode/latest/2.1.1/cli")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s request: %v", name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("%s response = %d %q", name, resp.StatusCode, body)
		}
	}

	head := <-firstHead
	if !strings.HasPrefix(head, "POST /v1/chat/completions HTTP/1.1\r\n") {
		t.Errorf("request line missing from head %q", head)
	}
	if !strings.Contains(head, "Authorization: Bearer public\r\n") {
		t.Errorf("common block missing Authorization in head %q", head)
	}
	if !strings.Contains(head, "Host: 127.0.0.1:"+strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")+"\r\n") {
		t.Errorf("Host must trail the head: %q", head)
	}
	if got := connections.Load(); got != 1 {
		t.Errorf("connections = %d, want the second request to reuse the pooled one", got)
	}
}

// readRawHead reads request head bytes up to the blank line.
func readRawHead(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		builder.WriteString(line)
		if line == "\r\n" {
			return builder.String(), nil
		}
	}
}

// serveOneBunExchange consumes the request body named by head and answers
// with a fixed keep-alive response.
func serveOneBunExchange(reader *bufio.Reader, conn net.Conn, head string) error {
	length := 0
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			_, _ = fmt.Sscanf(line, "Content-Length: %d", &length)
		}
	}
	if length > 0 {
		if _, err := io.ReadFull(reader, make([]byte, length)); err != nil {
			return err
		}
	}
	_, err := conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nok"))
	return err
}
