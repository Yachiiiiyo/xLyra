package app

import (
	"bytes"
	"compress/gzip"
	"errors"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"golang.org/x/sync/singleflight"
)

const (
	minCompressedStaticFileSize = int64(1024)
	staticCompressedCacheLimit  = int64(64 << 20)
)

var (
	errStaticCompressionUnavailable = errors.New("static compression unavailable")
	staticCompressibleTypes         = map[string]string{
		".js":          "text/javascript; charset=utf-8",
		".mjs":         "text/javascript; charset=utf-8",
		".css":         "text/css; charset=utf-8",
		".html":        "text/html; charset=utf-8",
		".json":        "application/json",
		".svg":         "image/svg+xml",
		".map":         "application/json",
		".txt":         "text/plain; charset=utf-8",
		".webmanifest": "application/manifest+json",
		".wasm":        "application/wasm",
	}
)

type compressedStaticFileKey struct {
	path     string
	size     int64
	modTime  int64
	encoding string
}

func (k compressedStaticFileKey) flightKey() string {
	return k.path + "\x00" + strconv.FormatInt(k.size, 10) + "\x00" + strconv.FormatInt(k.modTime, 10) + "\x00" + k.encoding
}

type compressedFileServer struct {
	staticDir string
	next      http.Handler

	mu           sync.Mutex
	cache        map[compressedStaticFileKey][]byte
	cachedBytes  int64
	cacheLimit   int64
	compressions int
	flights      singleflight.Group
}

func newCompressedFileServer(staticDir string, next http.Handler) *compressedFileServer {
	return &compressedFileServer{
		staticDir:  staticDir,
		next:       next,
		cache:      make(map[compressedStaticFileKey][]byte),
		cacheLimit: staticCompressedCacheLimit,
	}
}

func (s *compressedFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.next.ServeHTTP(w, r)
		return
	}
	path := filepath.Join(s.staticDir, filepath.Clean("/"+r.URL.Path))
	info, err := os.Stat(path)
	ext := strings.ToLower(filepath.Ext(path))
	fallbackType, compressible := staticCompressibleTypes[ext]
	if err != nil || !info.Mode().IsRegular() || !compressible || info.Size() < minCompressedStaticFileSize {
		s.next.ServeHTTP(w, r)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	encoding := preferredStaticEncoding(r.Header.Values("Accept-Encoding"))
	if encoding == "" {
		s.next.ServeHTTP(w, r)
		return
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		s.next.ServeHTTP(w, r)
		return
	}
	key := compressedStaticFileKey{
		path:     absolutePath,
		size:     info.Size(),
		modTime:  info.ModTime().UnixNano(),
		encoding: encoding,
	}
	data, ok := s.cached(key)
	if !ok {
		value, err, _ := s.flights.Do(key.flightKey(), func() (any, error) {
			if data, ok := s.cached(key); ok {
				return data, nil
			}
			s.mu.Lock()
			full := s.cacheLimit <= 0 || s.cachedBytes >= s.cacheLimit
			s.mu.Unlock()
			if full {
				return nil, errStaticCompressionUnavailable
			}
			data, err := compressStaticFile(key)
			if err != nil {
				return nil, err
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			s.compressions++
			if int64(len(data)) > s.cacheLimit-s.cachedBytes {
				return nil, errStaticCompressionUnavailable
			}
			s.cache[key] = data
			s.cachedBytes += int64(len(data))
			return data, nil
		})
		if err != nil {
			s.next.ServeHTTP(w, r)
			return
		}
		data = value.([]byte)
	}
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = fallbackType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Encoding", encoding)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", `W/"`+strconv.FormatInt(key.size, 10)+`-`+strconv.FormatInt(key.modTime, 10)+`-`+encoding+`"`)
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
}

func (s *compressedFileServer) cached(key compressedStaticFileKey) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.cache[key]
	return data, ok
}

func compressStaticFile(key compressedStaticFileKey) ([]byte, error) {
	source, err := os.ReadFile(key.path)
	if err != nil || int64(len(source)) != key.size {
		return nil, errStaticCompressionUnavailable
	}
	var output bytes.Buffer
	switch key.encoding {
	case "br":
		writer := brotli.NewWriterLevel(&output, 5)
		if _, err := writer.Write(source); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	case "gzip":
		writer, err := gzip.NewWriterLevel(&output, 6)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(source); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	default:
		return nil, errStaticCompressionUnavailable
	}
	info, err := os.Stat(key.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != key.size || info.ModTime().UnixNano() != key.modTime {
		return nil, errStaticCompressionUnavailable
	}
	return output.Bytes(), nil
}

func preferredStaticEncoding(headers []string) string {
	qualities := make(map[string]float64)
	for _, header := range headers {
		for _, value := range strings.Split(header, ",") {
			parts := strings.Split(value, ";")
			coding := strings.ToLower(strings.TrimSpace(parts[0]))
			if coding == "" {
				continue
			}
			quality := 1.0
			for _, parameter := range parts[1:] {
				name, value, ok := strings.Cut(parameter, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 || math.IsNaN(parsed) {
					quality = 0
				} else {
					quality = parsed
				}
			}
			qualities[coding] = quality
		}
	}
	quality := func(coding string) float64 {
		if value, ok := qualities[coding]; ok {
			return value
		}
		return qualities["*"]
	}
	bestQuality := 0.0
	if value, ok := qualities["identity"]; ok && value > 0 {
		bestQuality = value
	}
	encoding := ""
	if value := quality("br"); value > 0 && value >= bestQuality {
		encoding = "br"
		bestQuality = value
	}
	if value := quality("gzip"); value > 0 && value > bestQuality {
		encoding = "gzip"
	}
	return encoding
}
