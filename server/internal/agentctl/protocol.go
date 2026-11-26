package agentctl

import (
	"encoding/json"
	"net/http"
)

const (
	// ProtocolVersion is the current UDS control-plane wire protocol version.
	ProtocolVersion = 1

	// SchemaVersion identifies the supported operation/parameter contract revision.
	// Clients can inspect X-Xlyra-Ctl-Schema-Version or `status` to detect older servers.
	SchemaVersion = 2

	// HeaderProtocol carries the wire protocol version on every HTTP/UDS request.
	HeaderProtocol = "X-Xlyra-Ctl-Protocol"

	// HeaderNonce carries the per-run secret nonce in local (non-sandboxed) mode.
	HeaderNonce = "X-Xlyra-Ctl-Nonce"

	// HeaderCLIVersion optionally carries the calling xlyra CLI version.
	HeaderCLIVersion = "X-Xlyra-Cli-Version"

	// HeaderServerVersion carries the running xlyra-server version on every response.
	HeaderServerVersion = "X-Xlyra-Server-Version"

	// HeaderSchemaVersion carries SchemaVersion on every UDS response.
	HeaderSchemaVersion = "X-Xlyra-Ctl-Schema-Version"
)

// CLI exit codes mirrored from xLyra-cli/internal/protocol/types.go for clarity.
const (
	ExitOK             = 0
	ExitInternal       = 1
	ExitInvalid        = 2
	ExitNotFound       = 3
	ExitPolicyDenied   = 4
	ExitApprovalDenied = 5
	ExitConflict       = 6
)

// CallRequest is the POST /v1/call request body sent by xlyra CLI.
type CallRequest struct {
	Protocol  int            `json:"protocol"`
	Resource  string         `json:"resource"`
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	SubTarget string         `json:"sub_target,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
	Payload   any            `json:"payload,omitempty"`
	Options   CallOptions    `json:"options,omitempty"`
}

// CallOptions holds optional execution modifiers for a control call.
type CallOptions struct {
	DryRun  bool     `json:"dry_run,omitempty"`
	Fields  []string `json:"fields,omitempty"`
	IfMatch string   `json:"if_match,omitempty"`
}

// Envelope is the unified response wrapper returned by POST /v1/call.
type Envelope struct {
	OK    bool         `json:"ok"`
	Data  any          `json:"data,omitempty"`
	Diff  []DiffItem   `json:"diff,omitempty"`
	Meta  *Meta        `json:"meta,omitempty"`
	Error *ErrorDetail `json:"error,omitempty"`
}

// DiffItem describes a single mutated or proposed field change.
type DiffItem struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// Meta carries execution metadata such as risk tier, revision hash, and side effects.
type Meta struct {
	Tier           int      `json:"tier,omitempty"`
	DryRun         bool     `json:"dry_run"`
	Revision       string   `json:"revision,omitempty"`
	Truncated      bool     `json:"truncated"`
	NextPage       any      `json:"next_page,omitempty"`
	AppliedEffects []string `json:"applied_effects,omitempty"`
}

// ErrorDetail provides structured error diagnostics for agents and CLI callers.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// Well-known protocol error codes mapped to CLI exit codes.
const (
	ErrCodeInvalidArgument      = "invalid_argument"
	ErrCodeInvalidProtocol      = "invalid_protocol"
	ErrCodeUnauthorized         = "unauthorized"
	ErrCodeNotFound             = "not_found"
	ErrCodePolicyTier0Denied    = "policy_tier0_denied"
	ErrCodeCapabilityDenied     = "capability_denied"
	ErrCodeQuotaExceeded        = "quota_exceeded"
	ErrCodeApprovalDenied       = "approval_denied"
	ErrCodeUserDenied           = "user_denied"
	ErrCodeClassifierDenied     = "classifier_denied"
	ErrCodeApprovalTimeout      = "approval_timeout"
	ErrCodeConflict             = "conflict"
	ErrCodePreconditionFailed   = "precondition_failed"
	ErrCodeNotImplemented       = "not_implemented"
	ErrCodeInternal             = "internal_error"
)

type callError struct {
	Status int
	Tier   int
	Detail ErrorDetail
}

func (e *callError) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail.Code + ": " + e.Detail.Message
}

func newCallError(status int, code, message string) *callError {
	return &callError{
		Status: status,
		Detail: ErrorDetail{Code: code, Message: message},
	}
}

func (e *callError) withTier(tier int) *callError {
	e.Tier = tier
	return e
}

func (e *callError) withField(field string) *callError {
	e.Detail.Field = field
	return e
}

func (e *callError) withHint(hint string) *callError {
	e.Detail.Hint = hint
	return e
}

func errorEnvelope(err *callError) Envelope {
	var meta *Meta
	if err != nil && err.Tier > 0 {
		meta = &Meta{Tier: err.Tier}
	}
	var detail *ErrorDetail
	if err != nil {
		d := err.Detail
		detail = &d
	}
	return Envelope{
		OK:    false,
		Meta:  meta,
		Error: detail,
	}
}

func writeJSONEnvelope(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
