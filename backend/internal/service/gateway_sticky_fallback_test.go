//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGatewayBindStickySession_SkipsLowestGroupPriority(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)

	top := stickyFallbackTestAccount(17, groupID, 1)
	peerTop := stickyFallbackTestAccount(23, groupID, 1)
	fallback := stickyFallbackTestAccount(6, groupID, 2)
	repo := &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{
			top.ID:      &top,
			peerTop.ID:  &peerTop,
			fallback.ID: &fallback,
		},
		accountsByGroup: map[int64][]Account{
			groupID: {top, peerTop, fallback},
		},
	}
	cache := &mockGatewayCacheForPlatform{}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       cache,
	}

	err := svc.BindStickySession(ctx, &groupID, "session-fallback", fallback.ID)

	require.NoError(t, err)
	require.NotContains(t, cache.sessionBindings, "session-fallback")
}

func TestGatewayBindStickySession_AllowsHigherGroupPriority(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)

	top := stickyFallbackTestAccount(17, groupID, 1)
	fallback := stickyFallbackTestAccount(6, groupID, 2)
	repo := &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{
			top.ID:      &top,
			fallback.ID: &fallback,
		},
		accountsByGroup: map[int64][]Account{
			groupID: {top, fallback},
		},
	}
	cache := &mockGatewayCacheForPlatform{}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       cache,
	}

	err := svc.BindStickySession(ctx, &groupID, "session-top", top.ID)

	require.NoError(t, err)
	require.Equal(t, top.ID, cache.sessionBindings["session-top"])
}

func TestGatewayBindStickySession_AllowsOnlyPriorityTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)

	only := stickyFallbackTestAccount(17, groupID, 1)
	peer := stickyFallbackTestAccount(23, groupID, 1)
	repo := &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{
			only.ID: &only,
			peer.ID: &peer,
		},
		accountsByGroup: map[int64][]Account{
			groupID: {only, peer},
		},
	}
	cache := &mockGatewayCacheForPlatform{}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       cache,
	}

	err := svc.BindStickySession(ctx, &groupID, "session-single-tier", only.ID)

	require.NoError(t, err)
	require.Equal(t, only.ID, cache.sessionBindings["session-single-tier"])
}

func TestGatewayBindStickySession_CachesGroupPriorityRange(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)

	top := stickyFallbackTestAccount(17, groupID, 1)
	fallback := stickyFallbackTestAccount(6, groupID, 2)
	repo := &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{
			top.ID:      &top,
			fallback.ID: &fallback,
		},
		accountsByGroup: map[int64][]Account{
			groupID: {top, fallback},
		},
	}
	cache := &mockGatewayCacheForPlatform{}
	svc := NewGatewayService(repo, nil, nil, nil, nil, nil, nil, cache, testConfig(), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.BindStickySession(ctx, &groupID, "session-fallback-a", fallback.ID)
	require.NoError(t, err)
	err = svc.BindStickySession(ctx, &groupID, "session-fallback-b", fallback.ID)
	require.NoError(t, err)

	require.Equal(t, 1, repo.getByIDCalls)
	require.Equal(t, 1, repo.listByGroupCalls)
	require.NotContains(t, cache.sessionBindings, "session-fallback-a")
	require.NotContains(t, cache.sessionBindings, "session-fallback-b")
}

func TestGatewaySelectAccountWithLoadAwareness_DoesNotBindLowestPriorityFallback(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)
	sessionHash := "session-load-aware-fallback"

	top := stickyFallbackTestAccount(17, groupID, 1)
	fallback := stickyFallbackTestAccount(6, groupID, 2)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{top, fallback},
		accountsByID: map[int64]*Account{
			top.ID:      &top,
			fallback.ID: &fallback,
		},
		accountsByGroup: map[int64][]Account{
			groupID: {top, fallback},
		},
	}
	cache := &mockGatewayCacheForPlatform{}
	concurrencyCache := &mockConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			top.ID: {
				AccountID:          top.ID,
				CurrentConcurrency: top.Concurrency,
				LoadRate:           100,
			},
			fallback.ID: {
				AccountID: fallback.ID,
				LoadRate:  0,
			},
		},
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling = config.GatewaySchedulingConfig{
		LoadBatchEnabled: true,
	}
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:       groupID,
				Platform: PlatformAnthropic,
				Status:   StatusActive,
				Hydrated: true,
			},
		},
	}
	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          groupRepo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "claude-3-5-sonnet-20241022", nil, "", 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, fallback.ID, result.Account.ID)
	require.NotContains(t, cache.sessionBindings, sessionHash)
}

func stickyFallbackTestAccount(id int64, groupID int64, groupPriority int) Account {
	return Account{
		ID:          id,
		Name:        "account",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		AccountGroups: []AccountGroup{
			{AccountID: id, GroupID: groupID, Priority: groupPriority},
		},
	}
}
