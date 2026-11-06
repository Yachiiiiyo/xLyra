package runner

import (
	"errors"
	"testing"
)

func TestValidateMessagePermissionModes(t *testing.T) {
	r := &Runner{}
	for _, mode := range []string{"", "ask", "auto", "full"} {
		spec, _, err := r.validateMessage("内容", "mock-1", mode, "", nil)
		if err != nil {
			t.Errorf("validateMessage(mode=%q): %v", mode, err)
			continue
		}
		want := mode
		if want == "" {
			want = "ask"
		}
		if spec.mode != want {
			t.Errorf("validateMessage(mode=%q).mode = %q, want %q", mode, spec.mode, want)
		}
	}
	for _, mode := range []string{"Auto", "FULL", "bogus"} {
		if _, _, err := r.validateMessage("内容", "mock-1", mode, "", nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("validateMessage(mode=%q) err = %v, want ErrInvalid", mode, err)
		}
	}
}
