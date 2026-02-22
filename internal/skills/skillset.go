package skills

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"opencortex/internal/model"
)

const (
	TagSpecialSkillset = "_special:skillset"
	TagSkillSlugPrefix = "_skill_slug:"

	specialKnowledgeTypeKey = "type"
	specialKnowledgeTypeVal = "skillset"
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

	ErrNotSkillset = errors.New("not_skillset")
)

type InstallSpec struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Ref    string `json:"ref"`
	Method string `json:"method"`
}

type InstallPatch struct {
	Repo   *string `json:"repo"`
	Path   *string `json:"path"`
	Ref    *string `json:"ref"`
	Method *string `json:"method"`
}

type ParsedMetadata struct {
	Slug    string      `json:"slug"`
	Install InstallSpec `json:"install"`
}

type SkillView struct {
	model.KnowledgeEntry
	Slug    string      `json:"slug"`
	Install InstallSpec `json:"install"`
}

func NormalizeSlug(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", fmt.Errorf("slug is required")
	}

	var b strings.Builder
	lastHyphen := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if b.Len() == 0 || lastHyphen {
			continue
		}
		b.WriteByte('-')
		lastHyphen = true
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("slug is required")
	}
	if len(slug) > 64 {
		return "", fmt.Errorf("slug must be <= 64 characters")
	}
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("invalid slug")
	}
	return slug, nil
}

func ValidateInstallSpec(spec InstallSpec) (InstallSpec, error) {
	spec.Repo = strings.TrimSpace(spec.Repo)
	if !repoPattern.MatchString(spec.Repo) {
		return InstallSpec{}, fmt.Errorf("install.repo must be in owner/repo format")
	}

	cleanPath, err := normalizeInstallPath(spec.Path)
	if err != nil {
		return InstallSpec{}, err
	}
	spec.Path = cleanPath

	spec.Ref = strings.TrimSpace(spec.Ref)
	if spec.Ref == "" {
		spec.Ref = "main"
	}

	spec.Method = strings.ToLower(strings.TrimSpace(spec.Method))
	if spec.Method == "" {
		spec.Method = "auto"
	}
	switch spec.Method {
	case "auto", "download", "git":
	default:
		return InstallSpec{}, fmt.Errorf("install.method must be one of auto|download|git")
	}

	return spec, nil
}

func ApplyInstallPatch(base InstallSpec, patch *InstallPatch) (InstallSpec, error) {
	if patch == nil {
		return ValidateInstallSpec(base)
	}
	if patch.Repo != nil {
		base.Repo = *patch.Repo
	}
	if patch.Path != nil {
		base.Path = *patch.Path
	}
	if patch.Ref != nil {
		base.Ref = *patch.Ref
	}
	if patch.Method != nil {
		base.Method = *patch.Method
	}
	return ValidateInstallSpec(base)
}

func StripReservedTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if isReservedTag(tag) {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func BuildReservedTags(tags []string, slug string) []string {
	out := StripReservedTags(tags)
	out = append(out, TagSpecialSkillset, TagSkillSlugPrefix+slug)
	return out
}

func ExtractSlugFromTags(tags []string) string {
	for _, tag := range tags {
		if strings.HasPrefix(tag, TagSkillSlugPrefix) {
			return strings.TrimPrefix(tag, TagSkillSlugPrefix)
		}
	}
	return ""
}

func HasSkillTag(tags []string) bool {
	for _, tag := range tags {
		if tag == TagSpecialSkillset {
			return true
		}
	}
	return false
}

func ParseSkillMetadata(metadata map[string]any, tags []string) (ParsedMetadata, error) {
	if !HasSkillTag(tags) {
		return ParsedMetadata{}, ErrNotSkillset
	}

	slug := ExtractSlugFromTags(tags)
	if slug != "" {
		n, err := NormalizeSlug(slug)
		if err != nil {
			return ParsedMetadata{}, err
		}
		slug = n
	}

	if metadata == nil {
		metadata = map[string]any{}
	}

	if specialRaw, ok := metadata["special_knowledge"]; ok && specialRaw != nil {
		special, ok := asMap(specialRaw)
		if !ok {
			return ParsedMetadata{}, fmt.Errorf("metadata.special_knowledge must be an object")
		}
		if typ, _ := special[specialKnowledgeTypeKey].(string); typ != "" && !strings.EqualFold(typ, specialKnowledgeTypeVal) {
			return ParsedMetadata{}, ErrNotSkillset
		}
	}

	skillsetRaw, ok := metadata["skillset"]
	if !ok {
		return ParsedMetadata{}, fmt.Errorf("metadata.skillset is required")
	}
	skillsetMap, ok := asMap(skillsetRaw)
	if !ok {
		return ParsedMetadata{}, fmt.Errorf("metadata.skillset must be an object")
	}

	if rawSlug, _ := skillsetMap["slug"].(string); strings.TrimSpace(rawSlug) != "" {
		n, err := NormalizeSlug(rawSlug)
		if err != nil {
			return ParsedMetadata{}, err
		}
		slug = n
	}
	if slug == "" {
		return ParsedMetadata{}, fmt.Errorf("skill slug is required")
	}

	installRaw, ok := skillsetMap["install"]
	if !ok {
		return ParsedMetadata{}, fmt.Errorf("metadata.skillset.install is required")
	}
	installMap, ok := asMap(installRaw)
	if !ok {
		return ParsedMetadata{}, fmt.Errorf("metadata.skillset.install must be an object")
	}
	spec, err := ValidateInstallSpec(InstallSpec{
		Repo:   asString(installMap["repo"]),
		Path:   asString(installMap["path"]),
		Ref:    asString(installMap["ref"]),
		Method: asString(installMap["method"]),
	})
	if err != nil {
		return ParsedMetadata{}, err
	}

	return ParsedMetadata{Slug: slug, Install: spec}, nil
}

func BuildSkillMetadata(base map[string]any, slug string, install InstallSpec) map[string]any {
	out := copyMap(base)
	if out == nil {
		out = map[string]any{}
	}
	out["special_knowledge"] = map[string]any{
		"type":           specialKnowledgeTypeVal,
		"schema_version": 1,
	}
	out["skillset"] = map[string]any{
		"slug": slug,
		"install": map[string]any{
			"repo":   install.Repo,
			"path":   install.Path,
			"ref":    install.Ref,
			"method": install.Method,
		},
	}
	return out
}

func ToView(entry model.KnowledgeEntry) (SkillView, error) {
	parsed, err := ParseSkillMetadata(entry.Metadata, entry.Tags)
	if err != nil {
		return SkillView{}, err
	}
	return SkillView{
		KnowledgeEntry: entry,
		Slug:           parsed.Slug,
		Install:        parsed.Install,
	}, nil
}

func isReservedTag(tag string) bool {
	return tag == TagSpecialSkillset || strings.HasPrefix(tag, TagSkillSlugPrefix)
}

func normalizeInstallPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", fmt.Errorf("install.path is required")
	}
	if strings.HasPrefix(raw, "/") || driveLetterPath(raw) {
		return "", fmt.Errorf("install.path must be relative")
	}
	clean := path.Clean(raw)
	if clean == "." {
		return ".", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("install.path must stay within repository")
	}
	return clean, nil
}

func driveLetterPath(raw string) bool {
	return len(raw) >= 2 && ((raw[0] >= 'A' && raw[0] <= 'Z') || (raw[0] >= 'a' && raw[0] <= 'z')) && raw[1] == ':'
}

func copyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	switch vv := v.(type) {
	case map[string]any:
		return vv, true
	case map[any]any:
		out := make(map[string]any, len(vv))
		for k, val := range vv {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
