package performance

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	perfdom "github.com/suncrestlabs/nester/apps/api/internal/domain/performance"
)

// fakeAccuracySnapshotRepo stubs only the SnapshotRepository method
// GetProjectionAccuracy touches.
type fakeAccuracySnapshotRepo struct {
	perfdom.SnapshotRepository
	historyByVault map[uuid.UUID][]perfdom.APYDataPoint
}

func (f *fakeAccuracySnapshotRepo) APYHistoryForVault(_ context.Context, vaultID uuid.UUID, _ time.Time, _ string) ([]perfdom.APYDataPoint, error) {
	return f.historyByVault[vaultID], nil
}

func TestGetProjectionAccuracy_ComparesFirstAndLastBucket(t *testing.T) {
	vaultA := uuid.New()
	repo := &fakeAccuracySnapshotRepo{
		historyByVault: map[uuid.UUID][]perfdom.APYDataPoint{
			vaultA: {
				{Date: "2026-06-01", APY: "8.00"},
				{Date: "2026-06-15", APY: "8.30"},
				{Date: "2026-07-01", APY: "9.00"},
			},
		},
	}

	svc := &Service{repo: repo, clock: func() time.Time { return time.Now() }}

	summary, err := svc.GetProjectionAccuracy(context.Background(), []uuid.UUID{vaultA}, perfdom.Period30d)
	require.NoError(t, err)
	require.Len(t, summary.Points, 1)

	pt := summary.Points[0]
	assert.Equal(t, vaultA, pt.VaultID)
	assert.True(t, pt.Projected.Equal(decimal.NewFromFloat(8.00)))
	assert.True(t, pt.Realized.Equal(decimal.NewFromFloat(9.00)))
	assert.True(t, pt.ErrorPct.Equal(decimal.NewFromFloat(1.00)))
	assert.Equal(t, 1, summary.SampleCount)
	assert.True(t, summary.MeanErrorPct.Equal(decimal.NewFromFloat(1.00)))
	assert.True(t, summary.MeanAbsErrPct.Equal(decimal.NewFromFloat(1.00)))
}

func TestGetProjectionAccuracy_SkipsVaultsWithoutEnoughHistory(t *testing.T) {
	vaultA := uuid.New()
	vaultB := uuid.New()
	repo := &fakeAccuracySnapshotRepo{
		historyByVault: map[uuid.UUID][]perfdom.APYDataPoint{
			vaultA: {{Date: "2026-06-01", APY: "8.00"}}, // only one bucket
			vaultB: {
				{Date: "2026-06-01", APY: "5.00"},
				{Date: "2026-06-30", APY: "4.00"},
			},
		},
	}

	svc := &Service{repo: repo, clock: func() time.Time { return time.Now() }}

	summary, err := svc.GetProjectionAccuracy(context.Background(), []uuid.UUID{vaultA, vaultB}, perfdom.Period30d)
	require.NoError(t, err)
	require.Len(t, summary.Points, 1)
	assert.Equal(t, vaultB, summary.Points[0].VaultID)
	assert.True(t, summary.Points[0].ErrorPct.Equal(decimal.NewFromFloat(-1.00)))
}

func TestGetProjectionAccuracy_EmptyWhenNoVaults(t *testing.T) {
	repo := &fakeAccuracySnapshotRepo{historyByVault: map[uuid.UUID][]perfdom.APYDataPoint{}}
	svc := &Service{repo: repo, clock: func() time.Time { return time.Now() }}

	summary, err := svc.GetProjectionAccuracy(context.Background(), nil, perfdom.Period30d)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.SampleCount)
	assert.Empty(t, summary.Points)
}
