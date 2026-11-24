package agentctl

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type peerCredCtxKey struct{}

// UDSConfig configures a per-run Unix domain socket server bound to a single Engine and Caller.
type UDSConfig struct {
	SocketPath     string
	SocketMode     os.FileMode // defaults to 0o600
	SocketOwnerUID *int        // optional chown UID for isolated sandbox ExecUID
	SocketOwnerGID *int        // optional chown GID for isolated sandbox GID
	Nonce          string      // random per-run nonce; auto-generated if empty
	ExpectedUID    *uint32     // non-nil on Linux sandboxed isolation to enforce SO_PEERCRED UID match
	Caller         Caller
	Logger         *slog.Logger
}

// UDSServer exposes POST /v1/call over an isolated Unix domain socket for xlyra CLI.
type UDSServer struct {
	engine   *Engine
	cfg      UDSConfig
	listener net.Listener
	server   *http.Server
	wg       sync.WaitGroup
}

// StartUDSServer creates and starts a Unix domain socket listener for the given Engine and UDSConfig.
func StartUDSServer(engine *Engine, cfg UDSConfig) (*UDSServer, error) {
	if engine == nil {
		return nil, errors.New("agentctl: engine is nil")
	}
	if cfg.SocketPath == "" {
		return nil, errors.New("agentctl: socket path is required")
	}
	// Keep path compact enough for macOS 104-byte / Linux 108-byte sun_path limits.
	if len(cfg.SocketPath) >= 100 {
		return nil, fmt.Errorf("agentctl: unix socket path too long (%d bytes): %s", len(cfg.SocketPath), cfg.SocketPath)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o750); err != nil {
		return nil, fmt.Errorf("agentctl: create socket dir: %w", err)
	}
	_ = os.Remove(cfg.SocketPath)

	if cfg.Nonce == "" {
		nonce, err := GenerateNonce()
		if err != nil {
			return nil, fmt.Errorf("agentctl: generate nonce: %w", err)
		}
		cfg.Nonce = nonce
	}
	if cfg.SocketMode == 0 {
		cfg.SocketMode = 0o600
	}
	if cfg.Caller.Channel == "" {
		cfg.Caller.Channel = ChannelAgentCLI
	}
	if cfg.Caller.Quota == nil {
		cfg.Caller.Quota = NewRunQuota(60)
	}

	listener, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("agentctl: listen unix socket: %w", err)
	}
	if err := os.Chmod(cfg.SocketPath, cfg.SocketMode); err != nil {
		_ = listener.Close()
		_ = os.Remove(cfg.SocketPath)
		return nil, fmt.Errorf("agentctl: chmod unix socket: %w", err)
	}
	if cfg.SocketOwnerUID != nil || cfg.SocketOwnerGID != nil {
		uid := -1
		if cfg.SocketOwnerUID != nil {
			uid = *cfg.SocketOwnerUID
		}
		gid := -1
		if cfg.SocketOwnerGID != nil {
			gid = *cfg.SocketOwnerGID
		}
		if err := os.Chown(cfg.SocketPath, uid, gid); err != nil {
			_ = listener.Close()
			_ = os.Remove(cfg.SocketPath)
			return nil, fmt.Errorf("agentctl: chown unix socket: %w", err)
		}
	}

	s := &UDSServer{
		engine:   engine,
		cfg:      cfg,
		listener: listener,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/call", s.handleCall)

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, peerCredCtxKey{}, readPeerCred(c))
		},
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && cfg.Logger != nil {
			cfg.Logger.Warn("agentctl uds server stopped unexpectedly", "error", err)
		}
	}()
	return s, nil
}

// SocketPath returns the bound Unix domain socket path.
func (s *UDSServer) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.cfg.SocketPath
}

// Nonce returns the per-run authentication nonce expected in X-Xlyra-Ctl-Nonce.
func (s *UDSServer) Nonce() string {
	if s == nil {
		return ""
	}
	return s.cfg.Nonce
}

// Close shuts down the UDS server and deletes the socket file.
func (s *UDSServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	}
	err := s.server.Shutdown(ctx)
	s.wg.Wait()
	if rmErr := os.Remove(s.cfg.SocketPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

func (s *UDSServer) handleCall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(HeaderSchemaVersion, strconv.Itoa(SchemaVersion))
	if ver := s.engine.Deps().Version; ver != "" {
		w.Header().Set(HeaderServerVersion, ver)
	}

	if r.Method != http.MethodPost {
		writeJSONEnvelope(w, http.StatusMethodNotAllowed, errorEnvelope(
			newCallError(http.StatusMethodNotAllowed, ErrCodeInvalidArgument, "only POST /v1/call is supported"),
		))
		return
	}

	if s.cfg.ExpectedUID != nil {
		cred, _ := r.Context().Value(peerCredCtxKey{}).(peerCred)
		if !cred.Known || cred.UID != *s.cfg.ExpectedUID {
			writeJSONEnvelope(w, http.StatusForbidden, errorEnvelope(
				newCallError(http.StatusForbidden, ErrCodeUnauthorized, "peer UID mismatch on control socket"),
			))
			return
		}
	}

	if s.cfg.Nonce != "" {
		got := r.Header.Get(HeaderNonce)
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Nonce)) != 1 {
			writeJSONEnvelope(w, http.StatusForbidden, errorEnvelope(
				newCallError(http.StatusForbidden, ErrCodeUnauthorized, "invalid control socket nonce"),
			))
			return
		}
	}

	protoHeader := r.Header.Get(HeaderProtocol)
	if protoHeader == "" {
		writeJSONEnvelope(w, http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidProtocol, "missing "+HeaderProtocol+" header"),
		))
		return
	}
	parsedProto, err := strconv.Atoi(protoHeader)
	if err != nil || parsedProto != ProtocolVersion {
		writeJSONEnvelope(w, http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidProtocol,
				fmt.Sprintf("unsupported protocol header %q, want %d", protoHeader, ProtocolVersion)),
		))
		return
	}

	const maxReqBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReqBytes+1))
	if err != nil {
		writeJSONEnvelope(w, http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "failed to read request body"),
		))
		return
	}
	if len(body) > maxReqBytes {
		writeJSONEnvelope(w, http.StatusRequestEntityTooLarge, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "request body exceeds 1 MiB limit"),
		))
		return
	}

	var req CallRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONEnvelope(w, http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON request body: "+err.Error()),
		))
		return
	}

	status, env := s.engine.Execute(r.Context(), s.cfg.Caller, req)
	writeJSONEnvelope(w, status, env)
}

// GenerateNonce creates a 32-byte cryptographically random hex nonce.
func GenerateNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
