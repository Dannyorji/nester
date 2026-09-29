package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/suncrestlabs/nester/apps/api/internal/config"
)

func TestVerifyEndpointNetworks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	horizon := func(p string) endpointNetwork {
		return endpointNetwork{name: "horizon", url: "https://horizon.example", passphrase: p}
	}
	rpc := func(p string, err error) endpointNetwork {
		return endpointNetwork{name: "soroban rpc", url: "https://rpc.example", passphrase: p, err: err}
	}

	t.Run("matching endpoints boot", func(t *testing.T) {
		err := verifyEndpointNetworks(logger, config.MainnetPassphrase, true, []endpointNetwork{
			horizon(config.MainnetPassphrase), rpc(config.MainnetPassphrase, nil),
		})
		assert.NoError(t, err)
	})

	t.Run("mainnet config against testnet rpc refuses to boot", func(t *testing.T) {
		err := verifyEndpointNetworks(logger, config.MainnetPassphrase, true, []endpointNetwork{
			horizon(config.MainnetPassphrase), rpc(config.TestnetPassphrase, nil),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "soroban rpc at https://rpc.example serves network")
	})

	t.Run("testnet config against mainnet horizon refuses to boot", func(t *testing.T) {
		err := verifyEndpointNetworks(logger, config.TestnetPassphrase, false, []endpointNetwork{
			horizon(config.MainnetPassphrase), rpc(config.TestnetPassphrase, nil),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refusing to boot")
	})

	t.Run("unverifiable endpoint is fatal on mainnet", func(t *testing.T) {
		err := verifyEndpointNetworks(logger, config.MainnetPassphrase, true, []endpointNetwork{
			horizon(config.MainnetPassphrase), rpc("", errors.New("method not found")),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot verify it serves mainnet: method not found")
	})

	t.Run("unverifiable endpoint only warns off mainnet", func(t *testing.T) {
		err := verifyEndpointNetworks(logger, config.TestnetPassphrase, false, []endpointNetwork{
			horizon(""), rpc("", errors.New("method not found")),
		})
		assert.NoError(t, err)
	})
}
