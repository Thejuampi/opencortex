package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeCopilotMCPPreservesExistingKeys(t *testing.T) {
	input := []byte(`{"foo":"bar","mcpServers":{"other":{"command":"x"}}}`)
	out, changed, err := mergeCopilotMCP(input, "http://localhost:8080")
	if err != nil {
		t.Fatalf("mergeCopilotMCP error: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if got, _ := doc["foo"].(string); got != "bar" {
		t.Fatalf("expected foo preserved")
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcpServers object")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatalf("expected existing server preserved")
	}
	if _, ok := servers["opencortex"]; !ok {
		t.Fatalf("expected opencortex inserted")
	}
}

func TestMergeCodexConfigAppendsManagedBlock(t *testing.T) {
	input := "[foo]\nbar = 1\n"
	out, changed := mergeCodexConfig(input)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if !strings.Contains(out, "[foo]") {
		t.Fatalf("expected existing section preserved")
	}
	if !strings.Contains(out, "[mcp_servers.opencortex]") {
		t.Fatalf("expected opencortex section added")
	}
	if !strings.Contains(out, CodexBlockStart) || !strings.Contains(out, CodexBlockEnd) {
		t.Fatalf("expected managed markers")
	}
}

func TestMergeCodexConfigNoChangeWhenSectionExists(t *testing.T) {
	input := "[mcp_servers.opencortex]\ncommand = \"opencortex\"\nargs = [\"mcp\"]\n"
	out, changed := mergeCodexConfig(input)
	if changed {
		t.Fatalf("expected changed=false")
	}
	if out != input {
		t.Fatalf("expected identical output")
	}
}
