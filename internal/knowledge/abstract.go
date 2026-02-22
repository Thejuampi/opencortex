package knowledge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var mdNoise = regexp.MustCompile(`^[#>\-\*\d\.\)\(\[\]` + "`" + `\s]+`)
var inlineAbstractPrefix = regexp.MustCompile(`(?i)^~?(abstract|summary)\s*:\s*(.+)$`)

// GenerateAbstract builds a concise summary from markdown/plain content.
// Extraction order is deterministic and programmatic:
// 1. Front matter key at top: `summary:` or `abstract:`
// 2. First paragraph marker: `Abstract:` / `Summary:` / `~abstract:`
// 3. First meaningful paragraph fallback
// Result is normalized and capped to maxRunes.
func GenerateAbstract(content string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 280
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	if fmValue, body := frontMatterAbstract(content); fmValue != "" {
		return clip(normalizeLine(fmValue), maxRunes)
	} else {
		content = body
	}

	parts := strings.Split(content, "\n\n")
	for _, p := range parts {
		if isHeadingBlock(p) {
			continue
		}
		clean := normalizeLine(p)
		if clean == "" {
			continue
		}
		if v := inlineAbstract(clean); v != "" {
			return clip(v, maxRunes)
		}
		return clip(clean, maxRunes)
	}
	return ""
}

func frontMatterAbstract(content string) (abstract string, body string) {
	body = content
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return "", body
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(content, "---\r\n"), "---\n")
	end := strings.Index(trimmed, "\n---")
	if end < 0 {
		return "", body
	}
	fm := trimmed[:end]
	body = strings.TrimSpace(trimmed[end+4:])
	lines := strings.Split(fm, "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lower := strings.ToLower(ln)
		if strings.HasPrefix(lower, "summary:") || strings.HasPrefix(lower, "abstract:") {
			parts := strings.SplitN(ln, ":", 2)
			if len(parts) != 2 {
				continue
			}
			v := strings.TrimSpace(parts[1])
			v = strings.Trim(v, `"'`)
			if v != "" {
				return v, body
			}
		}
	}
	return "", body
}

func inlineAbstract(cleanParagraph string) string {
	m := inlineAbstractPrefix.FindStringSubmatch(cleanParagraph)
	if len(m) != 3 {
		return ""
	}
	return strings.TrimSpace(m[2])
}

func isHeadingBlock(in string) bool {
	lines := strings.Split(strings.TrimSpace(in), "\n")
	if len(lines) == 0 {
		return false
	}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "#") {
			return false
		}
	}
	return true
}

func normalizeLine(in string) string {
	lines := strings.Split(in, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		ln = mdNoise.ReplaceAllString(ln, "")
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, " ")
}

func clip(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}
