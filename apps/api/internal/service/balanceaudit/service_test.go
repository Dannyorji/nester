package balanceaudit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/transaction"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/vault"
)

type fakeVaultLister struct {
	vaults []vault.Vault
}

func (f *fakeVaultLister) ListVaults(_ context.Context, filter vault.ListFilter) ([]vault.Vault, int, error) {
	total := len(f.vaults)
	start := filter.Offset
	if start > total {
		start = total
	}
	end := start + filter.Limit
	if filter.Limit == 0 || end > total {
		end = total
	}
	return f.vaults[start:end], total, nil
}

type fakeTxRepo struct {
	transaction.Repository
	byVault map[uuid.UUID][]transaction.Transaction
}

func (f *fakeTxRepo) ListCompletedByVault(_ context.Context, vaultID uuid.UUID) ([]transaction.Transaction, error) {
	return f.byVault[vaultID], nil
}

func TestRun_FlagsVaultBelowNetPrincipalFloor(t *testing.T) {
	vaultID := uuid.New()
	vaults := &fakeVaultLister{vaults: []vault.Vault{
		{ID: vaultID, CurrentBalance: decimal.NewFromInt(50)}, // stored balance corrupted below floor
	}}
	txs := &fakeTxRepo{byVault: map[uuid.UUID][]transaction.Transaction{
		vaultID: {
			{VaultID: vaultID, Type: transaction.TypeDeposit, Amount: decimal.NewFromInt(100), Status: transaction.StatusCompleted},
			{VaultID: vaultID, Type: transaction.TypeWithdrawal, Amount: decimal.NewFromInt(20), Status: transaction.StatusCompleted},
		},
	}}

	svc := NewService(vaults, txs)
	svc.SetClock(func() time.Time { return time.Unix(0, 0) })

	result, err := svc.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.VaultsChecked)
	require.Len(t, result.Mismatches, 1)
	m := result.Mismatches[0]
	assert.Equal(t, vaultID, m.VaultID)
	assert.True(t, m.StoredBalance.Equal(decimal.NewFromInt(50)))
	assert.True(t, m.DerivedBalance.Equal(decimal.NewFromInt(80))) // 100 - 20
	assert.True(t, m.Difference.Equal(decimal.NewFromInt(30)))
	assert.Equal(t, 2, m.EntriesApplied)
}

func TestRun_NoMismatchWhenBalanceAboveFloor(t *testing.T) {
	vaultID := uuid.New()
	vaults := &fakeVaultLister{vaults: []vault.Vault{
		{ID: vaultID, CurrentBalance: decimal.NewFromInt(150)}, // includes accrued yield above principal
	}}
	txs := &fakeTxRepo{byVault: map[uuid.UUID][]transaction.Transaction{
		vaultID: {
			{VaultID: vaultID, Type: transaction.TypeDeposit, Amount: decimal.NewFromInt(100), Status: transaction.StatusCompleted},
		},
	}}

	svc := NewService(vaults, txs)
	result, err := svc.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.VaultsChecked)
	assert.Empty(t, result.Mismatches)
}

func TestRun_ChecksEveryVaultAcrossPages(t *testing.T) {
	var vaults []vault.Vault
	for i := 0; i < sweepPageSize+5; i++ {
		vaults = append(vaults, vault.Vault{ID: uuid.New(), CurrentBalance: decimal.Zero})
	}
	lister := &fakeVaultLister{vaults: vaults}
	txs := &fakeTxRepo{byVault: map[uuid.UUID][]transaction.Transaction{}}

	svc := NewService(lister, txs)
	result, err := svc.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, len(vaults), result.VaultsChecked)
	assert.Empty(t, result.Mismatches)
}
