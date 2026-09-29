package service

import (
	"context"
	"errors"
	"testing"
)

type recordingInvoker struct {
	NoopVaultChainInvoker
	paused, rebalanced bool
}

func (r *recordingInvoker) PauseVault(context.Context, string) error {
	r.paused = true
	return nil
}

func (r *recordingInvoker) RebalanceVault(context.Context, string) (string, error) {
	r.rebalanced = true
	return "tx", nil
}

func TestMultisigGuardedInvoker(t *testing.T) {
	inner := &recordingInvoker{}
	guard := NewMultisigGuardedInvoker(inner, "CMULTISIG")

	if err := guard.PauseVault(context.Background(), "CVAULT"); !errors.Is(err, ErrMultisigRequired) {
		t.Fatalf("PauseVault err = %v, want ErrMultisigRequired", err)
	}
	if err := guard.UnpauseVault(context.Background(), "CVAULT"); !errors.Is(err, ErrMultisigRequired) {
		t.Fatalf("UnpauseVault err = %v, want ErrMultisigRequired", err)
	}
	if inner.paused {
		t.Fatal("pause must not reach the single-key invoker")
	}

	if _, err := guard.RebalanceVault(context.Background(), "CVAULT"); err != nil || !inner.rebalanced {
		t.Fatalf("operator-level rebalance should pass through, err = %v", err)
	}
}

// Without an operator key the admin service falls back to the no-op invoker,
// whose PauseVault reports success. The guard must still refuse on mainnet.
func TestMultisigGuardedInvokerWrapsNil(t *testing.T) {
	guard := NewMultisigGuardedInvoker(nil, "CMULTISIG")
	if err := guard.PauseVault(context.Background(), "CVAULT"); !errors.Is(err, ErrMultisigRequired) {
		t.Fatalf("PauseVault err = %v, want ErrMultisigRequired", err)
	}
}
