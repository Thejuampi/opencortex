package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type KnowledgeEntry struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

type KnowledgeRequest struct {
	Title      string
	Content    string
	Tags       []string
	Summary    string
	Collection string
	ChangeNote string
}

type SearchQuery struct {
	Query string
	Tags  []string
	Limit int
	Page  int
}

type KnowledgeService struct {
	client *Client
}

func (k *KnowledgeService) Create(ctx context.Context, req KnowledgeRequest) (KnowledgeEntry, error) {
	var out struct {
		Knowledge KnowledgeEntry `json:"knowledge"`
	}
	err := k.client.do(ctx, http.MethodPost, "/api/v1/knowledge", map[string]any{
		"title":       req.Title,
		"content":     req.Content,
		"summary":     req.Summary,
		"tags":        req.Tags,
		"collection":  req.Collection,
		"change_note": req.ChangeNote,
	}, &out)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	return out.Knowledge, nil
}

func (k *KnowledgeService) Search(ctx context.Context, q SearchQuery) ([]KnowledgeEntry, error) {
	values := url.Values{}
	values.Set("q", q.Query)
	if len(q.Tags) > 0 {
		values.Set("tags", strings.Join(q.Tags, ","))
	}
	if q.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	if q.Page > 0 {
		values.Set("page", fmt.Sprintf("%d", q.Page))
	}
	var out struct {
		Knowledge []KnowledgeEntry `json:"knowledge"`
	}
	if err := k.client.do(ctx, http.MethodGet, "/api/v1/knowledge?"+values.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Knowledge, nil
}
