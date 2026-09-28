// Package balanceaudit implements the scheduled consistency sweep (issue
// #1338): for every vault, replay its completed deposit/withdrawal
// transactions (transaction.Repository — the vault's per-transaction audit
// trail) into a derived principal figure and compare it against the vault's
// stored current_balance.
//
// The sweep does not assert exact equality. current_balance also reflects
// continuously-accruing yield that has no corresponding transaction row, so
// an exact match would produce a false-positive mismatch on every healthy
// vault. What the transaction ledger *does* let us assert exactly is a hard
// invariant: current_balance can never be less than net principal
// (deposits minus withdrawals), because yield only ever adds to a vault's
// balance, never subtracts from principal. A vault whose stored balance
// falls below that floor indicates real corruption (a lost deposit, a
// double-counted withdrawal, a bad manual DB edit) and is flagged.
package balanceaudit

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/balanceaudit"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/transaction"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/vault"
)

// VaultLister is the subset of vault.Repository the sweep needs.
type VaultLister interface {
	ListVaults(ctx context.Context, filter vault.ListFilter) ([]vault.Vault, int, error)
}

// Service runs the sweep.
type Service struct {
	vaults VaultLister
	txs    transaction.Repository
	clock  func() time.Time
}

func NewService(vaults VaultLister, txs transaction.Repository) *Service {
	return &Service{vaults: vaults, txs: txs, clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock lets tests inject deterministic time.
func (s *Service) SetClock(clock func() time.Time) {
	s.clock = clock
}

// sweepPageSize bounds how many vaults are listed per ListVaults call while
// paging through the full vault set.
const sweepPageSize = 200

// Run walks every vault and flags any whose stored current_balance has
// fallen below its net-principal floor derived from completed transactions.
func (s *Service) Run(ctx context.Context) (balanceaudit.SweepResult, error) {
	result := balanceaudit.SweepResult{RunAt: s.clock()}

	offset := 0
	for {
		vaults, total, err := s.vaults.ListVaults(ctx, vault.ListFilter{Limit: sweepPageSize, Offset: offset})
		if err != nil {
			return result, fmt.Errorf("list vaults offset %d: %w", offset, err)
		}
		if len(vaults) == 0 {
			break
		}

		for _, v := range vaults {
			result.VaultsChecked++
			mismatch, err := s.checkVault(ctx, v)
			if err != nil {
				return result, fmt.Errorf("check vault %s: %w", v.ID, err)
			}
			if mismatch != nil {
				result.Mismatches = append(result.Mismatches, *mismatch)
			}
		}

		offset += len(vaults)
		if offset >= total || len(vaults) < sweepPageSize {
			break
		}
	}

	return result, nil
}

// checkVault replays v's completed transactions into a net-principal floor
// and returns a Mismatch if v.CurrentBalance has fallen below it.
func (s *Service) checkVault(ctx context.Context, v vault.Vault) (*balanceaudit.Mismatch, error) {
	txs, err := s.txs.ListCompletedByVault(ctx, v.ID)
	if err != nil {
		return nil, err
	}

	netPrincipal := decimal.Zero
	for _, tx := range txs {
		switch tx.Type {
		case transaction.TypeDeposit:
			netPrincipal = netPrincipal.Add(tx.Amount)
		case transaction.TypeWithdrawal:
			netPrincipal = netPrincipal.Sub(tx.Amount)
		}
	}

	if v.CurrentBalance.GreaterThanOrEqual(netPrincipal) {
		return nil, nil
	}

	diff := netPrincipal.Sub(v.CurrentBalance)
	return &balanceaudit.Mismatch{
		VaultID:        v.ID,
		StoredBalance:  v.CurrentBalance,
		DerivedBalance: netPrincipal,
		Difference:     diff,
		EntriesApplied: len(txs),
	}, nil
}
