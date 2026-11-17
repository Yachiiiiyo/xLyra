package jsplugin

import (
	"fmt"
	"strings"
)

// TransportSection is the optional manifest transport block. Profile selects a
// built-in TLS fingerprint. This build accepts only claude_code.
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
	case "claude_code":
		return nil
	case "bun":
		return fmt.Errorf("transport.profile %q is not supported by this build", profile)
	default:
		return fmt.Errorf("transport.profile %q is not supported (supported: claude_code)", profile)
	}
}
