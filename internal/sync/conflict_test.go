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
	got = ResolveStrategy(StrategyLatestWins, ConflictInput{
		LocalUpdatedAt:  now,
		RemoteUpdatedAt: now,
	})
	if got != StrategyLocalWins {
		t.Fatalf("expected local-wins on tie, got %s", got)
	}
}

func TestResolveStrategyExplicitAndDefault(t *testing.T) {
	in := ConflictInput{}
	if got := ResolveStrategy(StrategyLocalWins, in); got != StrategyLocalWins {
		t.Fatalf("expected local-wins passthrough, got %s", got)
	}
	if got := ResolveStrategy(StrategyRemoteWins, in); got != StrategyRemoteWins {
		t.Fatalf("expected remote-wins passthrough, got %s", got)
	}
	if got := ResolveStrategy(StrategyManual, in); got != StrategyManual {
		t.Fatalf("expected manual passthrough, got %s", got)
	}
	if got := ResolveStrategy(StrategyFork, in); got != StrategyFork {
		t.Fatalf("expected fork passthrough, got %s", got)
	}
	if got := ResolveStrategy(Strategy("unknown"), in); got != StrategyLatestWins {
		t.Fatalf("expected default latest-wins, got %s", got)
	}
}
