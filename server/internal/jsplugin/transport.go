package jsplugin

import (
	"fmt"
	"strings"
)

// TransportSection is the optional manifest transport block. Profile selects a
// built-in TLS fingerprint (claude_code or bun).
type TransportSection struct {
	Profile string `json:"profile,omitempty"`
}

// TransportProfile returns the declared profile, trimmed and lowercased.
func (m Manifest) TransportProfile() string {
	return strings.ToLower(strings.TrimSpace(m.Transport.Profile))
}

func validateTransport(section TransportSection) error {
	profile := strings.ToLower(strings.TrimSpace(section.Profile))
	if profile == "" {
		return nil
	}
	switch profile {
	case "claude_code", "bun":
		return nil
	default:
		return fmt.Errorf("transport.profile %q is not supported (supported: claude_code, bun)", profile)
	}
}
