// Package balanceaudit defines the domain types for the scheduled
// consistency sweep (issue #1338): walking every vault and comparing its
// stored current_balance against the balance derived by replaying that
// vault's audit-log entries (audit_logs, migration 011), flagging any
// mismatch for operator investigation.
package balanceaudit

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Mismatch is one vault whose audit-log-derived balance disagrees with its
// stored current_balance.
type Mismatch struct {
	VaultID        uuid.UUID       `json:"vault_id"`
	StoredBalance  decimal.Decimal `json:"stored_balance"`
	DerivedBalance decimal.Decimal `json:"derived_balance"`
	Difference     decimal.Decimal `json:"difference"`
	EntriesApplied int             `json:"entries_applied"`
}

// SweepResult is the outcome of one full run across all vaults.
type SweepResult struct {
	RunAt         time.Time  `json:"run_at"`
	VaultsChecked int        `json:"vaults_checked"`
	Mismatches    []Mismatch `json:"mismatches"`
}
