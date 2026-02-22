package knowledge

import "testing"

func TestBuildFTSQuery(t *testing.T) {
	if got := BuildFTSQuery("   "); got != "" {
		t.Fatalf("expected empty query, got %q", got)
	}
	if got := BuildFTSQuery(` "agent"   search "`); got != "agent   search " {
		t.Fatalf("unexpected sanitized query: %q", got)
	}
}

func TestNextVersion(t *testing.T) {
	if got := NextVersion(-1); got != 1 {
		t.Fatalf("expected 1 for negative input, got %d", got)
	}
	if got := NextVersion(7); got != 8 {
		t.Fatalf("expected 8, got %d", got)
	}
}

func TestFrontMatterAbstractEdgeCases(t *testing.T) {
	if v, body := frontMatterAbstract("no front matter"); v != "" || body != "no front matter" {
		t.Fatalf("unexpected parse without front matter: v=%q body=%q", v, body)
	}
	incomplete := "---\nsummary: hello\n"
	if v, body := frontMatterAbstract(incomplete); v != "" || body != incomplete {
		t.Fatalf("unexpected parse for incomplete front matter: v=%q body=%q", v, body)
	}
}

func TestInlineAbstractAndHeadingHelpers(t *testing.T) {
	if got := inlineAbstract("title only"); got != "" {
		t.Fatalf("expected empty inline abstract, got %q", got)
	}
	if got := inlineAbstract("Summary: quick line"); got != "quick line" {
		t.Fatalf("unexpected inline abstract: %q", got)
	}
	if !isHeadingBlock("# One\n## Two") {
		t.Fatal("expected heading block")
	}
	if isHeadingBlock("# One\nactual text") {
		t.Fatal("expected non-heading block")
	}
}

func TestNormalizeLineAndClip(t *testing.T) {
	in := "- first line\n# heading-ish\n  2) second line  "
	if got := normalizeLine(in); got != "first line heading-ish second line" {
		t.Fatalf("unexpected normalized line: %q", got)
	}
	if got := clip("a b c d", 1); got != "a" {
		t.Fatalf("unexpected clip len=1: %q", got)
	}
	if got := clip("hello world", 6); got != "hello…" {
		t.Fatalf("unexpected clipped text: %q", got)
	}
}
