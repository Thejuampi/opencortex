package syncer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"opencortex/internal/model"
	"opencortex/internal/storage/repos"
)

type Engine struct {
	DB        *sql.DB
	Store     *repos.Store
	Transport *Transport
}

func NewEngine(db *sql.DB, store *repos.Store) *Engine {
	return &Engine{
		DB:        db,
		Store:     store,
		Transport: NewTransport(),
	}
}

func (e *Engine) Diff(ctx context.Context, scope model.SyncScope, scopeIDs []string, remoteItems []ManifestItem) (need []ManifestItem, have []ManifestItem, err error) {
	local, err := BuildManifest(ctx, e.DB, scope, scopeIDs)
	if err != nil {
		return nil, nil, err
	}
	remoteMap := make(map[string]ManifestItem, len(remoteItems))
	for _, r := range remoteItems {
		remoteMap[r.EntityType+":"+r.ID] = r
	}
	localMap := make(map[string]ManifestItem, len(local))
	for _, l := range local {
		localMap[l.EntityType+":"+l.ID] = l
	}

	for _, l := range local {
		k := l.EntityType + ":" + l.ID
		r, ok := remoteMap[k]
		if !ok {
			need = append(need, l)
			continue
		}
		if r.Checksum != l.Checksum {
			need = append(need, l)
			have = append(have, r)
		}
	}
	for _, r := range remoteItems {
		if _, ok := localMap[r.EntityType+":"+r.ID]; !ok {
			have = append(have, r)
		}
	}
	return need, have, nil
}

func (e *Engine) Push(ctx context.Context, remoteName string, apiKey string, scope model.SyncScope, scopeIDs []string) (model.SyncLog, error) {
	manifest, _, strategy, err := e.Store.GetRemoteWithAuth(ctx, remoteName)
	if err != nil {
		return model.SyncLog{}, err
	}
	log, err := e.Store.CreateSyncLog(ctx, manifest.ID, model.SyncDirectionPush)
	if err != nil {
		return model.SyncLog{}, err
	}

	items, err := BuildManifest(ctx, e.DB, scope, scopeIDs)
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, err
	}
	key := apiKey
	if key == "" {
		msg := "remote api key is required for push"
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, errors.New(msg)
	}
	diffRes, err := e.Transport.Diff(ctx, manifest.RemoteURL, key, DiffRequest{
		Scope: string(scope),
		Items: items,
	})
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, err
	}

	conflicts := 0
	for _, item := range diffRes.Have {
		conflicts++
		_, _ = e.Store.CreateSyncConflict(ctx, manifest.ID, item.EntityType, item.ID, "", item.Checksum, strategy, nil, nil)
	}
	_, err = e.Transport.Push(ctx, manifest.RemoteURL, key, map[string]any{
		"remote": remoteName,
		"scope":  string(scope),
		"items":  diffRes.Need,
	})
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, conflicts, &msg)
		return model.SyncLog{}, err
	}
	status := model.SyncStatusSuccess
	if conflicts > 0 {
		status = model.SyncStatusPartial
	}
	_ = e.Store.CompleteSyncLog(ctx, log.ID, status, len(diffRes.Need), 0, conflicts, nil)
	_ = e.Store.UpdateManifestSyncResult(ctx, manifest.ID, true)
	return e.Store.GetSyncLog(ctx, log.ID)
}

func (e *Engine) Pull(ctx context.Context, remoteName string, apiKey string, scope model.SyncScope, scopeIDs []string) (model.SyncLog, error) {
	manifest, _, strategy, err := e.Store.GetRemoteWithAuth(ctx, remoteName)
	if err != nil {
		return model.SyncLog{}, err
	}
	log, err := e.Store.CreateSyncLog(ctx, manifest.ID, model.SyncDirectionPull)
	if err != nil {
		return model.SyncLog{}, err
	}
	items, err := BuildManifest(ctx, e.DB, scope, scopeIDs)
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, err
	}
	key := apiKey
	if key == "" {
		msg := "remote api key is required for pull"
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, errors.New(msg)
	}
	diffRes, err := e.Transport.Diff(ctx, manifest.RemoteURL, key, DiffRequest{
		Scope: string(scope),
		Items: items,
	})
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, err
	}

	_, err = e.Transport.Pull(ctx, manifest.RemoteURL, key, map[string]any{
		"remote": remoteName,
		"scope":  string(scope),
		"items":  diffRes.Have,
	})
	if err != nil {
		msg := err.Error()
		_ = e.Store.CompleteSyncLog(ctx, log.ID, model.SyncStatusFailed, 0, 0, 0, &msg)
		return model.SyncLog{}, err
	}
	conflicts := 0
	for _, item := range diffRes.Have {
		_, _ = e.Store.CreateSyncConflict(ctx, manifest.ID, item.EntityType, item.ID, "", item.Checksum, strategy, nil, nil)
		conflicts++
	}
	status := model.SyncStatusSuccess
	if conflicts > 0 {
		status = model.SyncStatusPartial
	}
	_ = e.Store.CompleteSyncLog(ctx, log.ID, status, 0, len(diffRes.Have), conflicts, nil)
	_ = e.Store.UpdateManifestSyncResult(ctx, manifest.ID, true)
	return e.Store.GetSyncLog(ctx, log.ID)
}

func (e *Engine) ResolveConflict(ctx context.Context, conflictID string, strategy string, note string) error {
	switch Strategy(strategy) {
	case StrategyLocalWins, StrategyRemoteWins, StrategyLatestWins, StrategyFork, StrategyManual:
	default:
		return fmt.Errorf("invalid strategy")
	}
	return e.Store.ResolveConflict(ctx, conflictID, strategy, note)
}

func simpleChecksum(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
