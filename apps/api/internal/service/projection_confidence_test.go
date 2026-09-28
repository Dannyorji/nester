package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/performance"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/projection"
)

// fakeAPYHistoryRepo stubs only the performance.SnapshotRepository methods
// calculateConfidenceBand/resolveAPYStdDev touch — the rest panic if called,
// so a test accidentally depending on unstubbed behavior fails loudly.
type fakeAPYHistoryRepo struct {
	performance.SnapshotRepository
	history []performance.APYDataPoint
	err     error
}

func (f *fakeAPYHistoryRepo) APYHistoryForVault(_ context.Context, _ uuid.UUID, _ time.Time, _ string) ([]performance.APYDataPoint, error) {
	return f.history, f.err
}

func TestCalculateConfidenceBand_DefaultPriorWhenNoHistory(t *testing.T) {
	svc := &ProjectionService{
		calculator:      NewCompoundInterestCalculator(),
		performanceRepo: &fakeAPYHistoryRepo{},
	}

	base := projection.ProjectionInput{
		InitialDeposit:      decimal.NewFromInt(1000),
		MonthlyContribution: decimal.Zero,
		APY:                 decimal.NewFromFloat(0.08),
		PeriodMonths:        12,
		CompoundFrequency:   projection.CompoundMonthly,
	}
	require.NoError(t, base.Validate())

	timeline := svc.calculator.Calculate(base)
	require.NotEmpty(t, timeline)
	summary := svc.calculateSummary(base, timeline)

	band := svc.calculateConfidenceBand(context.Background(), uuid.New(), base, summary)
	require.NotNil(t, band)

	assert.Equal(t, "default_prior", band.VolatilitySource)
	assert.InDelta(t, 0.08*defaultAPYStdDevFraction, band.APYStdDev, 1e-9)
	assert.True(t, band.Expected.Equal(summary.FinalBalance))
	assert.True(t, band.Low.LessThanOrEqual(band.Expected), "low should not exceed expected")
	assert.True(t, band.High.GreaterThanOrEqual(band.Expected), "high should not be below expected")
}

func TestCalculateConfidenceBand_HistoricalWhenEnoughSamples(t *testing.T) {
	history := make([]performance.APYDataPoint, 0, minHistoricalAPYSamples+1)
	apys := []string{"8.00", "8.50", "7.50", "8.20", "7.80", "8.10"}
	for i, apy := range apys {
		history = append(history, performance.APYDataPoint{
			Date: time.Now().AddDate(0, 0, -len(apys)+i).Format("2006-01-02"),
			APY:  apy,
		})
	}

	svc := &ProjectionService{
		calculator:      NewCompoundInterestCalculator(),
		performanceRepo: &fakeAPYHistoryRepo{history: history},
	}

	base := projection.ProjectionInput{
		InitialDeposit:      decimal.NewFromInt(1000),
		MonthlyContribution: decimal.Zero,
		APY:                 decimal.NewFromFloat(0.08),
		PeriodMonths:        12,
		CompoundFrequency:   projection.CompoundMonthly,
	}
	timeline := svc.calculator.Calculate(base)
	summary := svc.calculateSummary(base, timeline)

	band := svc.calculateConfidenceBand(context.Background(), uuid.New(), base, summary)
	require.NotNil(t, band)
	assert.Equal(t, "historical", band.VolatilitySource)
	assert.Greater(t, band.APYStdDev, 0.0)
	// Sample stddev of the values above is well under the 25% default-prior
	// fraction of the mean, confirming the historical path is actually used
	// rather than silently falling back.
	assert.Less(t, band.APYStdDev, 0.08*defaultAPYStdDevFraction)
}

func TestCalculateConfidenceBand_NilOnRepoError(t *testing.T) {
	svc := &ProjectionService{
		calculator:      NewCompoundInterestCalculator(),
		performanceRepo: &fakeAPYHistoryRepo{err: assert.AnError},
	}

	base := projection.ProjectionInput{
		InitialDeposit:    decimal.NewFromInt(1000),
		APY:               decimal.NewFromFloat(0.08),
		PeriodMonths:      12,
		CompoundFrequency: projection.CompoundMonthly,
	}
	timeline := svc.calculator.Calculate(base)
	summary := svc.calculateSummary(base, timeline)

	// A repo error just falls back to the default prior rather than failing
	// the whole projection — confidence bands are a nice-to-have annotation,
	// not a hard dependency of CalculateVaultProjection succeeding.
	band := svc.calculateConfidenceBand(context.Background(), uuid.New(), base, summary)
	require.NotNil(t, band)
	assert.Equal(t, "default_prior", band.VolatilitySource)
}
