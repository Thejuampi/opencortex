package install

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	skillmeta "opencortex/internal/skills"
)

type Request struct {
	Slug     string
	Install  skillmeta.InstallSpec
	Target   string
	Platform string
	Force    bool
}

type Result struct {
	Skill         string            `json:"skill"`
	CanonicalPath string            `json:"canonical_path"`
	Projections   map[string]string `json:"projections"`
	Warnings      []string          `json:"warnings,omitempty"`
}

func Install(ctx context.Context, req Request) (Result, error) {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if platform == "" {
		platform = "all"
	}
	switch platform {
	case "all", "codex", "copilot", "claude":
	default:
		return Result{}, fmt.Errorf("invalid platform %q", platform)
	}

	spec, err := skillmeta.ValidateInstallSpec(req.Install)
	if err != nil {
		return Result{}, err
	}
	slug, err := skillmeta.NormalizeSlug(req.Slug)
	if err != nil {
		return Result{}, err
	}

	root, err := resolveInstallRoot(req.Target)
	if err != nil {
		return Result{}, err
	}
	canonicalPath := filepath.Join(root, ".agents", "skills", slug)
	if existsPath(canonicalPath) {
		if !req.Force {
			return Result{}, fmt.Errorf("destination already exists: %s (use --force to replace)", canonicalPath)
		}
		if err := os.RemoveAll(canonicalPath); err != nil {
			return Result{}, err
		}
	}

	tmpDir, err := os.MkdirTemp("", "opencortex-skill-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmpDir)

	srcPath, err := fetchSkillSource(ctx, spec, tmpDir)
	if err != nil {
		return Result{}, err
	}
	if err := validateSkillFolder(srcPath); err != nil {
		return Result{}, err
	}
	if err := copyDir(srcPath, canonicalPath); err != nil {
		return Result{}, err
	}

	absCanonical, err := filepath.Abs(canonicalPath)
	if err != nil {
		absCanonical = canonicalPath
	}
	result := Result{
		Skill:         slug,
		CanonicalPath: absCanonical,
		Projections:   map[string]string{},
	}

	if platform == "all" || platform == "codex" {
		linkPath := filepath.Join(root, ".codex", "skills", slug)
		if err := ensureSymlink(absCanonical, linkPath, req.Force); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("codex projection failed: %v", err))
		} else {
			result.Projections["codex"] = linkPath
		}
	}

	skillMD := filepath.Join(absCanonical, "SKILL.md")
	if platform == "all" || platform == "copilot" {
		linkPath := filepath.Join(root, ".github", "copilot", slug+".md")
		if err := ensureSymlink(skillMD, linkPath, req.Force); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("copilot projection failed: %v", err))
		} else {
			result.Projections["copilot"] = linkPath
		}
	}
	if platform == "all" || platform == "claude" {
		linkPath := filepath.Join(root, ".claude", "skills", slug+".md")
		if err := ensureSymlink(skillMD, linkPath, req.Force); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("claude projection failed: %v", err))
		} else {
			result.Projections["claude"] = linkPath
		}
	}

	return result, nil
}

func resolveInstallRoot(target string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "repo":
		return ResolveGitRootOrCWD()
	case "global":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	default:
		return "", fmt.Errorf("invalid target %q (expected repo|global)", target)
	}
}

func ResolveGitRootOrCWD() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			return root, nil
		}
	}
	return os.Getwd()
}

func ensureSymlink(target, linkPath string, force bool) error {
	if existsPath(linkPath) {
		if !force {
			return fmt.Errorf("path exists: %s", linkPath)
		}
		if err := os.RemoveAll(linkPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

func fetchSkillSource(ctx context.Context, spec skillmeta.InstallSpec, tmpDir string) (string, error) {
	var (
		repoRoot string
		err      error
	)
	switch spec.Method {
	case "download":
		repoRoot, err = fetchRepoByDownload(ctx, spec, tmpDir)
	case "git":
		repoRoot, err = fetchRepoByGit(ctx, spec, tmpDir)
	case "auto":
		repoRoot, err = fetchRepoByDownload(ctx, spec, tmpDir)
		if err != nil {
			repoRoot, err = fetchRepoByGit(ctx, spec, tmpDir)
		}
	default:
		return "", fmt.Errorf("invalid install method %q", spec.Method)
	}
	if err != nil {
		return "", err
	}

	sourcePath := filepath.Join(repoRoot, filepath.FromSlash(spec.Path))
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skill path is not a directory: %s", sourcePath)
	}
	return sourcePath, nil
}

func fetchRepoByDownload(ctx context.Context, spec skillmeta.InstallSpec, tmpDir string) (string, error) {
	downloadURL := "https://codeload.github.com/" + spec.Repo + "/zip/" + url.PathEscape(spec.Ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "opencortex-skill-install")
	if tok := githubTokenFromEnv(); tok != "" {
		req.Header.Set("Authorization", "token "+tok)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	reader, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", err
	}

	dest := filepath.Join(tmpDir, "download")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	return ExtractZipArchive(reader, dest)
}

func fetchRepoByGit(ctx context.Context, spec skillmeta.InstallSpec, tmpDir string) (string, error) {
	repoDir := filepath.Join(tmpDir, "repo")
	httpsURL := "https://github.com/" + spec.Repo + ".git"
	sshURL := "git@github.com:" + spec.Repo + ".git"

	if err := cloneRepoWithMode(ctx, httpsURL, spec, repoDir); err != nil {
		if err2 := cloneRepoWithMode(ctx, sshURL, spec, repoDir); err2 != nil {
			return "", fmt.Errorf("git clone failed: %v; fallback failed: %v", err, err2)
		}
	}
	return repoDir, nil
}

func cloneRepoWithMode(ctx context.Context, repoURL string, spec skillmeta.InstallSpec, repoDir string) error {
	if err := os.RemoveAll(repoDir); err != nil {
		return err
	}

	if spec.Path == "." {
		if err := runGit(ctx, "clone", "--depth", "1", "--single-branch", "--branch", spec.Ref, repoURL, repoDir); err != nil {
			if err2 := os.RemoveAll(repoDir); err2 != nil {
				return err2
			}
			if err2 := runGit(ctx, "clone", "--depth", "1", "--single-branch", repoURL, repoDir); err2 != nil {
				return err2
			}
			return runGit(ctx, "-C", repoDir, "checkout", spec.Ref)
		}
		return nil
	}

	if err := runGit(ctx, "clone", "--filter=blob:none", "--depth", "1", "--sparse", "--single-branch", "--branch", spec.Ref, repoURL, repoDir); err != nil {
		if err2 := os.RemoveAll(repoDir); err2 != nil {
			return err2
		}
		if err2 := runGit(ctx, "clone", "--filter=blob:none", "--depth", "1", "--sparse", "--single-branch", repoURL, repoDir); err2 != nil {
			return err2
		}
		if err2 := runGit(ctx, "-C", repoDir, "checkout", spec.Ref); err2 != nil {
			return err2
		}
	}
	return runGit(ctx, "-C", repoDir, "sparse-checkout", "set", spec.Path)
}

func ExtractZipArchive(archive *zip.Reader, dest string) (string, error) {
	topLevels := map[string]struct{}{}
	for _, file := range archive.File {
		cleanName := filepath.Clean(file.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return "", fmt.Errorf("zip entry escaped destination: %s", file.Name)
		}
		parts := strings.Split(filepath.ToSlash(cleanName), "/")
		if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
			topLevels[parts[0]] = struct{}{}
		}
		dstPath := filepath.Join(dest, cleanName)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return "", err
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		fh, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			_ = rc.Close()
			return "", err
		}
		if _, err := io.Copy(fh, rc); err != nil {
			_ = fh.Close()
			_ = rc.Close()
			return "", err
		}
		_ = fh.Close()
		_ = rc.Close()
	}
	if len(topLevels) != 1 {
		return "", fmt.Errorf("unexpected archive layout")
	}
	for top := range topLevels {
		return filepath.Join(dest, top), nil
	}
	return "", fmt.Errorf("archive is empty")
}

func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func validateSkillFolder(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	skillPath := filepath.Join(path, "SKILL.md")
	info, err = os.Stat(skillPath)
	if err != nil {
		return fmt.Errorf("missing SKILL.md at %s", skillPath)
	}
	if info.IsDir() {
		return fmt.Errorf("SKILL.md is a directory at %s", skillPath)
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer dstFile.Close()
		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func existsPath(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func githubTokenFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}
