package syncer

import (
	"testing"
	"time"
)

func TestResolveStrategyLatestWins(t *testing.T) {
	now := time.Now().UTC()
	later := now.Add(time.Minute)

	got := ResolveStrategy(StrategyLatestWins, ConflictInput{
		LocalUpdatedAt:  later,
		RemoteUpdatedAt: now,
	})
	if got != StrategyLocalWins {
		t.Fatalf("expected local-wins, got %s", got)
	}
	got = ResolveStrategy(StrategyLatestWins, ConflictInput{
		LocalUpdatedAt:  now,
		RemoteUpdatedAt: later,
	})
	if got != StrategyRemoteWins {
		t.Fatalf("expected remote-wins, got %s", got)
	}
}
