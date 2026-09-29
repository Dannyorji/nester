package service

import (
	"context"
	"errors"
	"fmt"
)

// ErrMultisigRequired is returned for privileged on-chain operations that, on
// mainnet, must be approved by the N-of-M admin multisig rather than signed
// by the API's single operator key (#1374).
var ErrMultisigRequired = errors.New("operation requires admin multisig approval on mainnet")

// MultisigGuardedInvoker wraps a VaultChainInvoker on mainnet. Pause and
// unpause are Guardian/Admin operations held by the multisig account
// contract, so the API refuses them outright instead of attempting a
// single-key transaction that would fail on-chain — or, with the no-op
// invoker, report success while changing nothing on-chain. Operator-level
// calls (rebalance, allocation weights) pass through unchanged.
type MultisigGuardedInvoker struct {
	VaultChainInvoker
	MultisigAddress string
}

// NewMultisigGuardedInvoker guards inner (NoopVaultChainInvoker when nil).
func NewMultisigGuardedInvoker(inner VaultChainInvoker, multisigAddress string) MultisigGuardedInvoker {
	if inner == nil {
		inner = NoopVaultChainInvoker{}
	}
	return MultisigGuardedInvoker{VaultChainInvoker: inner, MultisigAddress: multisigAddress}
}

func (m MultisigGuardedInvoker) PauseVault(_ context.Context, contractAddress string) error {
	return fmt.Errorf("%w: submit pause for %s through multisig %s", ErrMultisigRequired, contractAddress, m.MultisigAddress)
}

func (m MultisigGuardedInvoker) UnpauseVault(_ context.Context, contractAddress string) error {
	return fmt.Errorf("%w: submit unpause for %s through multisig %s", ErrMultisigRequired, contractAddress, m.MultisigAddress)
}
