package skills

import (
	"testing"
	"time"

	"opencortex/internal/model"
)

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "My Skill", want: "my-skill"},
		{in: "  skill__name  ", want: "skill-name"},
		{in: "abc-123", want: "abc-123"},
		{in: "----", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := NormalizeSlug(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("NormalizeSlug(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeSlug(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeSlug(%q): got %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateInstallSpec(t *testing.T) {
	spec, err := ValidateInstallSpec(InstallSpec{
		Repo: "openai/skills",
		Path: "skills/.curated/example",
		Ref:  "",
	})
	if err != nil {
		t.Fatalf("ValidateInstallSpec: %v", err)
	}
	if spec.Ref != "main" {
		t.Fatalf("expected default ref main, got %s", spec.Ref)
	}
	if spec.Method != "auto" {
		t.Fatalf("expected default method auto, got %s", spec.Method)
	}

	if _, err := ValidateInstallSpec(InstallSpec{
		Repo: "openai/skills",
		Path: "../escape",
	}); err == nil {
		t.Fatalf("expected path traversal error")
	}

	if _, err := ValidateInstallSpec(InstallSpec{
		Repo: "bad repo",
		Path: "skills/sample",
	}); err == nil {
		t.Fatalf("expected invalid repo error")
	}
}

func TestToView(t *testing.T) {
	entry := model.KnowledgeEntry{
		ID:        "k1",
		Title:     "Skill",
		Content:   "content",
		Tags:      []string{TagSpecialSkillset, TagSkillSlugPrefix + "my-skill"},
		Metadata:  map[string]any{"note": "x"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	meta := BuildSkillMetadata(entry.Metadata, "my-skill", InstallSpec{
		Repo:   "openai/skills",
		Path:   "skills/.curated/my-skill",
		Ref:    "main",
		Method: "auto",
	})
	entry.Metadata = meta

	view, err := ToView(entry)
	if err != nil {
		t.Fatalf("ToView: %v", err)
	}
	if view.Slug != "my-skill" {
		t.Fatalf("unexpected slug %s", view.Slug)
	}
	if view.Install.Repo != "openai/skills" {
		t.Fatalf("unexpected install repo %s", view.Install.Repo)
	}
}
