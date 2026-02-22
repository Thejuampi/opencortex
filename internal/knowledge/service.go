package knowledge

import (
	"context"

	"opencortex/internal/model"
	"opencortex/internal/storage/repos"
)

type Service struct {
	Store *repos.Store
}

func New(store *repos.Store) *Service {
	return &Service{Store: store}
}

func (s *Service) Search(ctx context.Context, f repos.KnowledgeFilters) ([]model.KnowledgeEntry, int, error) {
	return s.Store.SearchKnowledge(ctx, f)
}
