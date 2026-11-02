package marketproto

import "errors"

// Error codes shared with xLyra. revoked is reserved for callers that map a
// security disable onto an error; this package reports revocation as effects.
const (
	CodeBadEnvelope        = "bad_envelope"
	CodeBadPayloadType     = "bad_payload_type"
	CodeBadSignature       = "bad_signature"
	CodeUnknownKey         = "unknown_key"
	CodeKeyRole            = "key_role"
	CodeKeyExpired         = "key_expired"
	CodeSeqRollback        = "seq_rollback"
	CodeSeqConflict        = "seq_conflict"
	CodeExpired            = "expired"
	CodeUnsupportedVersion = "unsupported_version"
	CodeDigestMismatch     = "digest_mismatch"
	CodeFileHashMismatch   = "file_hash_mismatch"
	CodeDeveloperMismatch  = "developer_mismatch"
	CodeManifestMismatch   = "manifest_mismatch"
	CodeRevoked            = "revoked"
	CodeBadVersion         = "bad_version"
	CodeBadField           = "bad_field"
)

// Error is a protocol rejection. Code is one of the constants above.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

// Code returns the protocol error code, or "" when err is nil or not an Error.
func Code(err error) string {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func errCode(code, msg string) error {
	return &Error{Code: code, Msg: msg}
}
