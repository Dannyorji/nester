package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/balanceaudit"
)

// BalanceSweepRunner is a narrow interface exposing only the sweep
// capability from balanceaudit's service, so the scheduler package doesn't
// need the full service dependency.
type BalanceSweepRunner interface {
	Run(ctx context.Context) (balanceaudit.SweepResult, error)
}

// BalanceAuditSweepConfig controls how often the sweep job runs.
type BalanceAuditSweepConfig struct {
	// Enabled disables the job entirely when false.
	Enabled bool
	// Interval is how often the sweep runs. Recommended: every few hours —
	// this is a background consistency check, not a real-time alert path.
	Interval time.Duration
}

// BalanceMismatchAlerter is invoked when a sweep finds one or more vaults
// whose stored balance disagrees with their derived balance.
type BalanceMismatchAlerter interface {
	AlertBalanceMismatch(ctx context.Context, result balanceaudit.SweepResult)
}

type BalanceMismatchAlertFunc func(ctx context.Context, result balanceaudit.SweepResult)

func (f BalanceMismatchAlertFunc) AlertBalanceMismatch(ctx context.Context, result balanceaudit.SweepResult) {
	f(ctx, result)
}

// BalanceAuditSweep is a background job that periodically walks every vault
// and flags any whose audit-log-derived balance disagrees with its stored
// balance (issue #1338).
type BalanceAuditSweep struct {
	cfg     BalanceAuditSweepConfig
	runner  BalanceSweepRunner
	alerter BalanceMismatchAlerter
	logger  *slog.Logger
}

func NewBalanceAuditSweep(
	cfg BalanceAuditSweepConfig,
	runner BalanceSweepRunner,
	alerter BalanceMismatchAlerter,
	logger *slog.Logger,
) *BalanceAuditSweep {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	return &BalanceAuditSweep{cfg: cfg, runner: runner, alerter: alerter, logger: logger}
}

// Run drives the sweep loop until the context is cancelled. When Enabled is
// false Run returns immediately.
func (s *BalanceAuditSweep) Run(ctx context.Context) {
	if !s.cfg.Enabled {
		s.logger.Info("balance audit sweep: disabled, not starting")
		return
	}

	interval := s.cfg.Interval
	if interval == 0 {
		interval = 6 * time.Hour
	}

	s.logger.Info("balance audit sweep: starting", "interval", interval)
	s.runOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("balance audit sweep: stopping")
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// RunOnce performs a single sweep pass. Exposed for manual operator runs.
func (s *BalanceAuditSweep) RunOnce(ctx context.Context) (balanceaudit.SweepResult, error) {
	return s.runner.Run(ctx)
}

func (s *BalanceAuditSweep) runOnce(ctx context.Context) {
	start := time.Now()
	result, err := s.RunOnce(ctx)
	elapsed := time.Since(start)

	if err != nil {
		s.logger.Error("balance audit sweep: run failed", "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}

	if len(result.Mismatches) > 0 {
		s.logger.Error("balance audit sweep: mismatches found",
			"vaults_checked", result.VaultsChecked,
			"mismatch_count", len(result.Mismatches),
			"elapsed_ms", elapsed.Milliseconds(),
		)
		if s.alerter != nil {
			s.alerter.AlertBalanceMismatch(ctx, result)
		}
		return
	}

	s.logger.Info("balance audit sweep: OK",
		"vaults_checked", result.VaultsChecked,
		"elapsed_ms", elapsed.Milliseconds(),
	)
}
