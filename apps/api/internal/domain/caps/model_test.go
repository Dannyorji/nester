package caps

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCheckVaultCap(t *testing.T) {
	cap := decimal.RequireFromString("1000")

	tests := []struct {
		name    string
		balance string
		cap     *decimal.Decimal
		amount  string
		wantErr error
	}{
		{name: "no cap set allows anything", balance: "999", cap: nil, amount: "1000000"},
		{name: "within cap", balance: "500", cap: &cap, amount: "400"},
		{name: "exactly at cap", balance: "500", cap: &cap, amount: "500"},
		{name: "exceeds cap", balance: "500", cap: &cap, amount: "500.01", wantErr: ErrVaultCapExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckVaultCap(
				decimal.RequireFromString(tt.balance),
				tt.cap,
				decimal.RequireFromString(tt.amount),
			)
			if !errors.Is(err, tt.wantErr) && err != tt.wantErr {
				t.Fatalf("CheckVaultCap() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckUserDailyCap(t *testing.T) {
	cap := decimal.RequireFromString("5000")

	tests := []struct {
		name    string
		rolling string
		cap     *decimal.Decimal
		amount  string
		wantErr error
	}{
		{name: "no cap set allows anything", rolling: "4999", cap: nil, amount: "1000000"},
		{name: "within cap", rolling: "1000", cap: &cap, amount: "2000"},
		{name: "exactly at cap", rolling: "3000", cap: &cap, amount: "2000"},
		{name: "exceeds cap", rolling: "3000", cap: &cap, amount: "2000.01", wantErr: ErrUserDailyCapExceeded},
		{name: "zero rolling total still bounded", rolling: "0", cap: &cap, amount: "5000.01", wantErr: ErrUserDailyCapExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckUserDailyCap(
				decimal.RequireFromString(tt.rolling),
				tt.cap,
				decimal.RequireFromString(tt.amount),
			)
			if !errors.Is(err, tt.wantErr) && err != tt.wantErr {
				t.Fatalf("CheckUserDailyCap() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
