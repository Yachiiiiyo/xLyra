package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInitTestAndDryRunGatewayPipelineTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-pipeline")
	chosen, err := chooseTemplate("gateway-pipeline", "")
	if err != nil {
		t.Fatalf("chooseTemplate: %v", err)
	}
	if err := scaffold(dir, chosen, "acme.my-pipeline", "My Pipeline"); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if !testOnce(dir) {
		t.Fatal("testOnce failed for scaffolded gateway-pipeline template")
	}

	out, err := executeDryRun(dir, "", "gpt-4o", map[string]any{"token": true, "dictionary": "AcmeSecret"}, nil, []byte("hello tok_abcdef123456 and AcmeSecret"))
	if err != nil {
		t.Fatalf("executeDryRun: %v", err)
	}
	if out["outcome"] != "transformed" || out["changed"] != true {
		t.Fatalf("dry-run out = %#v", out)
	}
	transformed, _ := out["transformed_text"].(string)
	if !strings.Contains(transformed, "[[TOKEN_1]]") || !strings.Contains(transformed, "[[CUSTOM_1]]") {
		t.Fatalf("transformed_text = %q", transformed)
	}
}
