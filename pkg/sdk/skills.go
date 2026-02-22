package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type SkillInstall struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Ref    string `json:"ref"`
	Method string `json:"method"`
}

type Skill struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	ContentType string         `json:"content_type"`
	Summary     *string        `json:"summary,omitempty"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata"`
	Slug        string         `json:"slug"`
	Install     SkillInstall   `json:"install"`
	Version     int            `json:"version"`
}

type SkillVersion struct {
	ID          string  `json:"id"`
	KnowledgeID string  `json:"knowledge_id"`
	Version     int     `json:"version"`
	Content     string  `json:"content"`
	Summary     *string `json:"summary,omitempty"`
	ChangedBy   string  `json:"changed_by"`
	ChangeNote  *string `json:"change_note,omitempty"`
}

type SkillCreateRequest struct {
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	ContentType string         `json:"content_type,omitempty"`
	Summary     *string        `json:"summary,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Slug        string         `json:"slug,omitempty"`
	Install     SkillInstall   `json:"install"`
}

type SkillReplaceRequest struct {
	Content     string         `json:"content"`
	ContentType string         `json:"content_type,omitempty"`
	Summary     *string        `json:"summary,omitempty"`
	ChangeNote  *string        `json:"change_note,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Slug        *string        `json:"slug,omitempty"`
	Install     map[string]any `json:"install,omitempty"`
}

type SkillPatchRequest struct {
	Summary  *string        `json:"summary,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Slug     *string        `json:"slug,omitempty"`
	Install  map[string]any `json:"install,omitempty"`
}

type SkillsQuery struct {
	Query string
	Tags  []string
	Limit int
	Page  int
}

type SkillsService struct {
	client *Client
}

func (s *SkillsService) Create(ctx context.Context, req SkillCreateRequest) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/skills", req, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}

func (s *SkillsService) List(ctx context.Context, q SkillsQuery) ([]Skill, error) {
	values := url.Values{}
	if strings.TrimSpace(q.Query) != "" {
		values.Set("q", q.Query)
	}
	if len(q.Tags) > 0 {
		values.Set("tags", strings.Join(q.Tags, ","))
	}
	if q.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	if q.Page > 0 {
		values.Set("page", fmt.Sprintf("%d", q.Page))
	}
	path := "/api/v1/skills"
	if qs := values.Encode(); qs != "" {
		path += "?" + qs
	}
	var out struct {
		Skills []Skill `json:"skills"`
	}
	if err := s.client.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Skills, nil
}

func (s *SkillsService) Get(ctx context.Context, id string) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodGet, "/api/v1/skills/"+id, nil, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}

func (s *SkillsService) Replace(ctx context.Context, id string, req SkillReplaceRequest) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodPut, "/api/v1/skills/"+id, req, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}

func (s *SkillsService) Patch(ctx context.Context, id string, req SkillPatchRequest) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodPatch, "/api/v1/skills/"+id, req, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}

func (s *SkillsService) Delete(ctx context.Context, id string) error {
	return s.client.do(ctx, http.MethodDelete, "/api/v1/skills/"+id, nil, nil)
}

func (s *SkillsService) History(ctx context.Context, id string) ([]SkillVersion, error) {
	var out struct {
		History []SkillVersion `json:"history"`
	}
	if err := s.client.do(ctx, http.MethodGet, "/api/v1/skills/"+id+"/history", nil, &out); err != nil {
		return nil, err
	}
	return out.History, nil
}

func (s *SkillsService) Version(ctx context.Context, id string, version int) (SkillVersion, error) {
	var out struct {
		Version SkillVersion `json:"version"`
	}
	path := fmt.Sprintf("/api/v1/skills/%s/versions/%d", id, version)
	if err := s.client.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SkillVersion{}, err
	}
	return out.Version, nil
}

func (s *SkillsService) Pin(ctx context.Context, id string) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodPost, "/api/v1/skills/"+id+"/pin", map[string]any{}, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}

func (s *SkillsService) Unpin(ctx context.Context, id string) (Skill, error) {
	var out struct {
		Skill Skill `json:"skill"`
	}
	if err := s.client.do(ctx, http.MethodDelete, "/api/v1/skills/"+id+"/pin", nil, &out); err != nil {
		return Skill{}, err
	}
	return out.Skill, nil
}
