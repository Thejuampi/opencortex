package syncer

import "time"

type Strategy string

const (
	StrategyLocalWins  Strategy = "local-wins"
	StrategyRemoteWins Strategy = "remote-wins"
	StrategyLatestWins Strategy = "latest-wins"
	StrategyManual     Strategy = "manual"
	StrategyFork       Strategy = "fork"
)

type ConflictInput struct {
	LocalUpdatedAt  time.Time
	RemoteUpdatedAt time.Time
}

func ResolveStrategy(strategy Strategy, in ConflictInput) Strategy {
	switch strategy {
	case StrategyLocalWins, StrategyRemoteWins, StrategyManual, StrategyFork:
		return strategy
	case StrategyLatestWins:
		if in.LocalUpdatedAt.After(in.RemoteUpdatedAt) || in.LocalUpdatedAt.Equal(in.RemoteUpdatedAt) {
			return StrategyLocalWins
		}
		return StrategyRemoteWins
	default:
		return StrategyLatestWins
	}
}
