// Package caps enforces deposit limits: a per-vault soft capacity and a
// per-user rolling 24h cap across all vaults (nester#1316). Kept
// dependency-free, like domain/moneypath, so both the service layer and the
// postgres repository can depend on it without an import cycle.
package caps

import (
	"errors"

	"github.com/shopspring/decimal"
)

// ErrVaultCapExceeded is returned when crediting a deposit would push a
// vault's current_balance past its soft_capacity.
var ErrVaultCapExceeded = errors.New("deposit would exceed vault capacity limit")

// ErrUserDailyCapExceeded is returned when a deposit would push a user's
// trailing-24h deposit total past their daily_deposit_cap.
var ErrUserDailyCapExceeded = errors.New("deposit would exceed user daily deposit limit")

// CheckVaultCap reports whether depositing amount into a vault currently at
// currentBalance would exceed cap. A nil cap means no limit.
func CheckVaultCap(currentBalance decimal.Decimal, cap *decimal.Decimal, amount decimal.Decimal) error {
	if cap == nil {
		return nil
	}
	if currentBalance.Add(amount).GreaterThan(*cap) {
		return ErrVaultCapExceeded
	}
	return nil
}

// CheckUserDailyCap reports whether depositing amount, on top of a user's
// existing rolling24hTotal, would exceed cap. A nil cap means no limit.
func CheckUserDailyCap(rolling24hTotal decimal.Decimal, cap *decimal.Decimal, amount decimal.Decimal) error {
	if cap == nil {
		return nil
	}
	if rolling24hTotal.Add(amount).GreaterThan(*cap) {
		return ErrUserDailyCapExceeded
	}
	return nil
}
