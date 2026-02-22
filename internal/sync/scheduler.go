package syncer

import (
	"context"
	"log"
	"sync"

	"github.com/robfig/cron/v3"

	"opencortex/internal/config"
	"opencortex/internal/model"
)

type Scheduler struct {
	cron     *cron.Cron
	engine   *Engine
	mu       sync.Mutex
	inflight map[string]struct{}
}

func NewScheduler(engine *Engine) *Scheduler {
	return &Scheduler{
		cron:     cron.New(),
		engine:   engine,
		inflight: map[string]struct{}{},
	}
}

func (s *Scheduler) Register(remotes []config.Remote) error {
	for _, r := range remotes {
		spec := r.Sync.Schedule
		if spec == "" {
			continue
		}
		remote := r
		_, err := s.cron.AddFunc(spec, func() {
			s.runRemote(remote)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) Start() {
	s.cron.Start()
}

func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

func (s *Scheduler) runRemote(remote config.Remote) {
	s.mu.Lock()
	if _, ok := s.inflight[remote.Name]; ok {
		s.mu.Unlock()
		return
	}
	s.inflight[remote.Name] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, remote.Name)
		s.mu.Unlock()
	}()

	scope := model.SyncScope(remote.Sync.Scope)
	if scope == "" {
		scope = model.SyncScopeFull
	}
	switch model.SyncDirection(remote.Sync.Direction) {
	case model.SyncDirectionPush:
		if _, err := s.engine.Push(context.Background(), remote.Name, remote.Key, scope, remote.Sync.CollectionIDs); err != nil {
			log.Printf("sync push failed for %s: %v", remote.Name, err)
		}
	case model.SyncDirectionPull:
		if _, err := s.engine.Pull(context.Background(), remote.Name, remote.Key, scope, remote.Sync.CollectionIDs); err != nil {
			log.Printf("sync pull failed for %s: %v", remote.Name, err)
		}
	default:
		if _, err := s.engine.Push(context.Background(), remote.Name, remote.Key, scope, remote.Sync.CollectionIDs); err != nil {
			log.Printf("sync push failed for %s: %v", remote.Name, err)
		}
		if _, err := s.engine.Pull(context.Background(), remote.Name, remote.Key, scope, remote.Sync.CollectionIDs); err != nil {
			log.Printf("sync pull failed for %s: %v", remote.Name, err)
		}
	}
}
