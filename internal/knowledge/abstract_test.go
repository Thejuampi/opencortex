package knowledge

import (
	"strings"
	"testing"
)

func TestGenerateAbstractUsesFirstMeaningfulParagraph(t *testing.T) {
	in := `# Title

This is the first meaningful paragraph with details.

## Next
Other stuff.`
	got := GenerateAbstract(in, 280)
	if got == "" {
		t.Fatal("expected non-empty abstract")
	}
	if !strings.Contains(got, "first meaningful paragraph") {
		t.Fatalf("unexpected abstract: %q", got)
	}
}

func TestGenerateAbstractClips(t *testing.T) {
	in := strings.Repeat("a", 100)
	got := GenerateAbstract(in, 20)
	if len([]rune(got)) > 20 {
		t.Fatalf("expected clipped abstract <= 20 runes, got %d", len([]rune(got)))
	}
}

func TestGenerateAbstractUsesFrontMatter(t *testing.T) {
	in := `---
title: Demo
summary: This abstract comes from front matter.
---

# Heading

Body paragraph that should not win.`

	got := GenerateAbstract(in, 280)
	if got != "This abstract comes from front matter." {
		t.Fatalf("unexpected abstract from front matter: %q", got)
	}
}

func TestGenerateAbstractUsesInlineMarker(t *testing.T) {
	in := `# Notes

Abstract: Deterministic short explanation for agents.

More details below.`

	got := GenerateAbstract(in, 280)
	if got != "Deterministic short explanation for agents." {
		t.Fatalf("unexpected inline abstract: %q", got)
	}
}
