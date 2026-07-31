package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterOpenAIHighestPriorityWithCapacity(t *testing.T) {
	candidate := func(id int64, priority, concurrency, current int, loadKnown bool) openAIAccountCandidateScore {
		return openAIAccountCandidateScore{
			account: &Account{ID: id, Priority: priority, Concurrency: concurrency},
			loadInfo: &AccountLoadInfo{
				AccountID:          id,
				CurrentConcurrency: current,
			},
			loadKnown: loadKnown,
		}
	}

	t.Run("keeps traffic in highest priority tier while it has capacity", func(t *testing.T) {
		got := filterOpenAIHighestPriorityWithCapacity([]openAIAccountCandidateScore{
			candidate(1, 0, 2, 1, true),
			candidate(2, 10, 10, 0, true),
		})
		require.Len(t, got, 1)
		require.Equal(t, int64(1), got[0].account.ID)
	})

	t.Run("overflows only after higher priority tier is full", func(t *testing.T) {
		got := filterOpenAIHighestPriorityWithCapacity([]openAIAccountCandidateScore{
			candidate(1, 0, 2, 2, true),
			candidate(2, 10, 10, 0, true),
		})
		require.Len(t, got, 1)
		require.Equal(t, int64(2), got[0].account.ID)
	})

	t.Run("unknown higher priority load fails closed", func(t *testing.T) {
		got := filterOpenAIHighestPriorityWithCapacity([]openAIAccountCandidateScore{
			candidate(1, 0, 2, 99, false),
			candidate(2, 10, 10, 0, true),
		})
		require.Len(t, got, 1)
		require.Equal(t, int64(1), got[0].account.ID)
	})

	t.Run("keeps all candidates when every tier is full for existing wait fallback", func(t *testing.T) {
		input := []openAIAccountCandidateScore{
			candidate(1, 0, 1, 1, true),
			candidate(2, 10, 1, 1, true),
		}
		got := filterOpenAIHighestPriorityWithCapacity(input)
		require.Equal(t, input, got)
	})
}
