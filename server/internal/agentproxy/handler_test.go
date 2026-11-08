package agentproxy

import (
	"testing"
)

func TestValidateAttachmentsChecksLimitsAndDataURLs(t *testing.T) {
	valid := map[string]any{"name": "a.png", "mime_type": "image/png", "data_url": "data:image/png;base64,AA=="}

	if err := ValidateAttachments(nil); err != nil {
		t.Fatalf("nil attachments: %v", err)
	}
	if err := ValidateAttachments([]any{valid}); err != nil {
		t.Fatalf("valid attachment: %v", err)
	}
	if err := ValidateAttachments("nope"); err == nil {
		t.Fatal("non-array attachments should be rejected")
	}
	oversized := make([]any, maxAgentAttachments+1)
	for i := range oversized {
		oversized[i] = valid
	}
	if err := ValidateAttachments(oversized); err == nil {
		t.Fatal("too many attachments should be rejected")
	}
	badName := map[string]any{"name": " ", "mime_type": "image/png", "data_url": "data:image/png;base64,AA=="}
	if err := ValidateAttachments([]any{badName}); err == nil {
		t.Fatal("empty attachment name should be rejected")
	}
	badDataURL := map[string]any{"name": "a.png", "mime_type": "image/png", "data_url": "not-a-data-url"}
	if err := ValidateAttachments([]any{badDataURL}); err == nil {
		t.Fatal("non-data-url attachment should be rejected")
	}
	invalidBase64 := map[string]any{"name": "a.png", "mime_type": "image/png", "data_url": "data:image/png;base64,****"}
	if err := ValidateAttachments([]any{invalidBase64}); err == nil {
		t.Fatal("invalid base64 attachment should be rejected")
	}
}
