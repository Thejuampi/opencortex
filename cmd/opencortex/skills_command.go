package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	skillmeta "opencortex/internal/skills"
	skillinstall "opencortex/internal/skills/install"
)

type cliSkill struct {
	ID      string                `json:"id"`
	Title   string                `json:"title"`
	Slug    string                `json:"slug"`
	Install skillmeta.InstallSpec `json:"install"`
}

type skillInstallResult struct {
	Skill         string            `json:"skill"`
	CanonicalPath string            `json:"canonical_path"`
	Projections   map[string]string `json:"projections"`
	Warnings      []string          `json:"warnings,omitempty"`
}

func newSkillsCommand(cfgPath, baseURL, apiKey *string, asJSON *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage shared skillsets",
		Long:  "Manage skillsets stored as special knowledge entries and install canonical/projection links locally.",
		Example: strings.TrimSpace(`
  opencortex skills --help
  opencortex skills list
  opencortex skills add --title "openapi-review" --slug openapi-review --file ./SKILL.md --repo openai/skills --path skills/.curated/openapi-review
  opencortex skills install openapi-review
  opencortex skills install 2b0f4f5d-... --target global --platform codex`),
	}

	var listQuery, listTags string
	var listLimit, listPage int
	cmdList := &cobra.Command{
		Use:   "list",
		Short: "List skillsets",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			v := url.Values{}
			if strings.TrimSpace(listQuery) != "" {
				v.Set("q", listQuery)
			}
			if strings.TrimSpace(listTags) != "" {
				v.Set("tags", listTags)
			}
			if listLimit > 0 {
				v.Set("limit", fmt.Sprintf("%d", listLimit))
			}
			if listPage > 0 {
				v.Set("page", fmt.Sprintf("%d", listPage))
			}
			path := "/api/v1/skills"
			if qs := v.Encode(); qs != "" {
				path += "?" + qs
			}
			var out map[string]any
			if err := client.do(http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdList.Flags().StringVar(&listQuery, "q", "", "Full-text query")
	cmdList.Flags().StringVar(&listTags, "tags", "", "Comma-separated tags")
	cmdList.Flags().IntVar(&listLimit, "limit", 20, "Page size")
	cmdList.Flags().IntVar(&listPage, "page", 1, "Page number")
	cmd.AddCommand(cmdList)

	cmd.AddCommand(&cobra.Command{
		Use:   "search <query>",
		Short: "Search skillsets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			path := "/api/v1/skills?q=" + url.QueryEscape(args[0])
			if err := client.do(http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "get <id-or-slug-or-title>",
		Short: "Get a skillset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/skills/"+url.PathEscape(skill.ID), nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	var addTitle, addFile, addSummary, addSlug string
	var addTags []string
	var addRepo, addPath, addRef, addMethod string
	cmdAdd := &cobra.Command{
		Use:   "add",
		Short: "Create a skillset",
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := os.ReadFile(addFile)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"title":   addTitle,
				"content": string(content),
				"tags":    addTags,
				"slug":    addSlug,
				"install": map[string]any{
					"repo":   addRepo,
					"path":   addPath,
					"ref":    addRef,
					"method": addMethod,
				},
			}
			if strings.TrimSpace(addSummary) != "" {
				payload["summary"] = addSummary
			}
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/skills", payload, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdAdd.Flags().StringVar(&addTitle, "title", "", "Skill title")
	cmdAdd.Flags().StringVar(&addFile, "file", "", "Path to SKILL.md content")
	cmdAdd.Flags().StringVar(&addSummary, "summary", "", "Optional summary")
	cmdAdd.Flags().StringSliceVar(&addTags, "tags", nil, "Tags")
	cmdAdd.Flags().StringVar(&addSlug, "slug", "", "Skill slug (defaults to normalized title)")
	cmdAdd.Flags().StringVar(&addRepo, "repo", "", "GitHub repo (owner/repo)")
	cmdAdd.Flags().StringVar(&addPath, "path", "", "Skill path inside repo")
	cmdAdd.Flags().StringVar(&addRef, "ref", "main", "Git ref")
	cmdAdd.Flags().StringVar(&addMethod, "method", "auto", "Fetch method: auto|download|git")
	_ = cmdAdd.MarkFlagRequired("title")
	_ = cmdAdd.MarkFlagRequired("file")
	_ = cmdAdd.MarkFlagRequired("repo")
	_ = cmdAdd.MarkFlagRequired("path")
	cmd.AddCommand(cmdAdd)

	var updFile, updSummary, updChangeNote, updSlug string
	var updTags []string
	var updRepo, updPath, updRef, updMethod string
	cmdUpdate := &cobra.Command{
		Use:   "update <id-or-slug-or-title>",
		Short: "Replace skill content and metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := os.ReadFile(updFile)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"content": string(content),
			}
			if strings.TrimSpace(updSummary) != "" {
				payload["summary"] = updSummary
			}
			if strings.TrimSpace(updChangeNote) != "" {
				payload["change_note"] = updChangeNote
			}
			if strings.TrimSpace(updSlug) != "" {
				payload["slug"] = updSlug
			}
			if cmd.Flags().Changed("tags") {
				payload["tags"] = updTags
			}
			installPatch := map[string]any{}
			if strings.TrimSpace(updRepo) != "" {
				installPatch["repo"] = updRepo
			}
			if strings.TrimSpace(updPath) != "" {
				installPatch["path"] = updPath
			}
			if strings.TrimSpace(updRef) != "" {
				installPatch["ref"] = updRef
			}
			if strings.TrimSpace(updMethod) != "" {
				installPatch["method"] = updMethod
			}
			if len(installPatch) > 0 {
				payload["install"] = installPatch
			}

			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodPut, "/api/v1/skills/"+url.PathEscape(skill.ID), payload, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdUpdate.Flags().StringVar(&updFile, "file", "", "Path to content file")
	cmdUpdate.Flags().StringVar(&updSummary, "summary", "", "Summary")
	cmdUpdate.Flags().StringVar(&updChangeNote, "note", "", "Change note")
	cmdUpdate.Flags().StringSliceVar(&updTags, "tags", nil, "Tags")
	cmdUpdate.Flags().StringVar(&updSlug, "slug", "", "Skill slug")
	cmdUpdate.Flags().StringVar(&updRepo, "repo", "", "GitHub repo (owner/repo)")
	cmdUpdate.Flags().StringVar(&updPath, "path", "", "Skill path inside repo")
	cmdUpdate.Flags().StringVar(&updRef, "ref", "", "Git ref")
	cmdUpdate.Flags().StringVar(&updMethod, "method", "", "Fetch method: auto|download|git")
	_ = cmdUpdate.MarkFlagRequired("file")
	cmd.AddCommand(cmdUpdate)

	var patchSummary, patchSlug string
	var patchTags []string
	var patchRepo, patchPath, patchRef, patchMethod string
	cmdPatch := &cobra.Command{
		Use:   "patch <id-or-slug-or-title>",
		Short: "Patch skill metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{}
			if strings.TrimSpace(patchSummary) != "" {
				payload["summary"] = patchSummary
			}
			if strings.TrimSpace(patchSlug) != "" {
				payload["slug"] = patchSlug
			}
			if cmd.Flags().Changed("tags") {
				payload["tags"] = patchTags
			}
			installPatch := map[string]any{}
			if strings.TrimSpace(patchRepo) != "" {
				installPatch["repo"] = patchRepo
			}
			if strings.TrimSpace(patchPath) != "" {
				installPatch["path"] = patchPath
			}
			if strings.TrimSpace(patchRef) != "" {
				installPatch["ref"] = patchRef
			}
			if strings.TrimSpace(patchMethod) != "" {
				installPatch["method"] = patchMethod
			}
			if len(installPatch) > 0 {
				payload["install"] = installPatch
			}
			if len(payload) == 0 {
				return errors.New("nothing to patch")
			}

			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodPatch, "/api/v1/skills/"+url.PathEscape(skill.ID), payload, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmdPatch.Flags().StringVar(&patchSummary, "summary", "", "Summary")
	cmdPatch.Flags().StringSliceVar(&patchTags, "tags", nil, "Tags")
	cmdPatch.Flags().StringVar(&patchSlug, "slug", "", "Skill slug")
	cmdPatch.Flags().StringVar(&patchRepo, "repo", "", "GitHub repo (owner/repo)")
	cmdPatch.Flags().StringVar(&patchPath, "path", "", "Skill path inside repo")
	cmdPatch.Flags().StringVar(&patchRef, "ref", "", "Git ref")
	cmdPatch.Flags().StringVar(&patchMethod, "method", "", "Fetch method: auto|download|git")
	cmd.AddCommand(cmdPatch)

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <id-or-slug-or-title>",
		Short: "Delete a skillset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodDelete, "/api/v1/skills/"+url.PathEscape(skill.ID), nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "history <id-or-slug-or-title>",
		Short: "Show skill history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/skills/"+url.PathEscape(skill.ID)+"/history", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "version <id-or-slug-or-title> <v>",
		Short: "Show specific skill version",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodGet, "/api/v1/skills/"+url.PathEscape(skill.ID)+"/versions/"+args[1], nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "pin <id-or-slug-or-title>",
		Short: "Pin a skillset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodPost, "/api/v1/skills/"+url.PathEscape(skill.ID)+"/pin", map[string]any{}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "unpin <id-or-slug-or-title>",
		Short: "Unpin a skillset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(http.MethodDelete, "/api/v1/skills/"+url.PathEscape(skill.ID)+"/pin", nil, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	})

	var installTarget string
	var installPlatform string
	var installForce bool
	cmdInstall := &cobra.Command{
		Use:   "install <id-or-slug-or-title>",
		Short: "Install a skillset locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAutoClientWithEnsure(*baseURL, *apiKey, *cfgPath)
			if err != nil {
				return err
			}
			skill, err := resolveSkillSelector(client, args[0])
			if err != nil {
				return err
			}
			result, err := installSkillLocally(cmd.Context(), skill, installTarget, installPlatform, installForce)
			if err != nil {
				return err
			}
			if *asJSON {
				return printJSON(result)
			}
			fmt.Printf("Installed %s to %s\n", result.Skill, result.CanonicalPath)
			keys := make([]string, 0, len(result.Projections))
			for k := range result.Projections {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Printf("  %s -> %s\n", key, result.Projections[key])
			}
			for _, warn := range result.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
			}
			return nil
		},
	}
	cmdInstall.Flags().StringVar(&installTarget, "target", "repo", "Install target: repo|global")
	cmdInstall.Flags().StringVar(&installPlatform, "platform", "all", "Projection target: all|codex|copilot|claude")
	cmdInstall.Flags().BoolVar(&installForce, "force", false, "Replace existing canonical/projection paths")
	cmd.AddCommand(cmdInstall)

	return cmd
}

func resolveSkillSelector(client *apiClient, selector string) (cliSkill, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return cliSkill{}, errors.New("selector is required")
	}

	var getOut struct {
		Skill cliSkill `json:"skill"`
	}
	getErr := client.do(http.MethodGet, "/api/v1/skills/"+url.PathEscape(selector), nil, &getOut)
	if getErr == nil {
		return getOut.Skill, nil
	}
	if !strings.Contains(getErr.Error(), "NOT_FOUND") {
		return cliSkill{}, getErr
	}

	var listOut struct {
		Skills []cliSkill `json:"skills"`
	}
	path := "/api/v1/skills?q=" + url.QueryEscape(selector) + "&limit=100"
	if err := client.do(http.MethodGet, path, nil, &listOut); err != nil {
		return cliSkill{}, err
	}
	if len(listOut.Skills) == 0 {
		return cliSkill{}, fmt.Errorf("skill %q not found", selector)
	}

	var slugMatches []cliSkill
	var titleMatches []cliSkill
	for _, skill := range listOut.Skills {
		if strings.EqualFold(skill.Slug, selector) {
			slugMatches = append(slugMatches, skill)
		}
		if strings.EqualFold(skill.Title, selector) {
			titleMatches = append(titleMatches, skill)
		}
	}
	if len(slugMatches) == 1 {
		return slugMatches[0], nil
	}
	if len(slugMatches) > 1 {
		return cliSkill{}, fmt.Errorf("ambiguous selector %q: multiple slug matches, use id", selector)
	}
	if len(titleMatches) == 1 {
		return titleMatches[0], nil
	}
	if len(titleMatches) > 1 {
		return cliSkill{}, fmt.Errorf("ambiguous selector %q: multiple title matches, use id", selector)
	}
	return cliSkill{}, fmt.Errorf("skill %q not found by exact id, slug, or title", selector)
}

func installSkillLocally(ctx context.Context, skill cliSkill, target, platform string, force bool) (skillInstallResult, error) {
	result, err := skillinstall.Install(ctx, skillinstall.Request{
		Slug:     skill.Slug,
		Install:  skill.Install,
		Target:   target,
		Platform: platform,
		Force:    force,
	})
	if err != nil {
		return skillInstallResult{}, err
	}
	return skillInstallResult(result), nil
}

func resolveGitRootOrCWD() (string, error) {
	return skillinstall.ResolveGitRootOrCWD()
}

func extractZipArchive(archive *zip.Reader, dest string) (string, error) {
	return skillinstall.ExtractZipArchive(archive, dest)
}
