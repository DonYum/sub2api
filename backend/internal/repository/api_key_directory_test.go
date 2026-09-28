package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAPIKeyDirectoryExactOwnerFilters(t *testing.T) {
	ctx := context.Background()
	repo, client := newAPIKeyRepoSQLite(t)
	owner := mustCreateAPIKeyRepoUser(t, ctx, client, "directory-owner@example.com")
	other := mustCreateAPIKeyRepoUser(t, ctx, client, "directory-other@example.com")
	own, err := client.APIKey.Create().SetUserID(owner.ID).SetKey("owner-token").SetName("alpha").Save(ctx)
	require.NoError(t, err)
	foreign, err := client.APIKey.Create().SetUserID(other.ID).SetKey("foreign-token").SetName("alpha").Save(ctx)
	require.NoError(t, err)
	_, err = client.APIKey.Create().SetUserID(owner.ID).SetKey("contains-alpha-token").SetName("alphabet").Save(ctx)
	require.NoError(t, err)
	_, err = client.APIKey.Create().SetUserID(owner.ID).SetKey("deleted-token").SetName("alpha").SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	name := "alpha"
	params := pagination.PaginationParams{Page: 1, PageSize: 2, SortBy: "id", SortOrder: "asc"}
	rows, page, err := repo.ListByUserID(ctx, owner.ID, params, service.APIKeyListFilters{ExactName: &name})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, rows, 1)
	require.Equal(t, own.ID, rows[0].ID)
	rows, _, err = repo.ListByUserID(ctx, owner.ID, params, service.APIKeyListFilters{ExactName: &name, ID: &foreign.ID})
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = client.APIKey.Create().SetUserID(owner.ID).SetKey("duplicate-token").SetName("alpha").Save(ctx)
	require.NoError(t, err)
	rows, page, err = repo.ListByUserID(ctx, owner.ID, params, service.APIKeyListFilters{ExactName: &name})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.EqualValues(t, 2, page.Total)
}
