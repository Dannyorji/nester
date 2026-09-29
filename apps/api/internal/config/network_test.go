package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	testMultisigAddress = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	mainnetUSDCIssuer   = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
)

// mainnetEnv sets a complete, valid mainnet profile from the process
// environment, run from a directory with no .env file.
func mainnetEnv(t *testing.T) {
	t.Helper()
	baseEnv(t)
	requiredEnv(t)
	chdir(t, t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("ALLOWED_ORIGINS", "https://app.nester.finance")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_live_dummy")
	t.Setenv("STELLAR_NETWORK", NetworkMainnet)
	t.Setenv("STELLAR_NETWORK_PASSPHRASE", MainnetPassphrase)
	t.Setenv("STELLAR_RPC_URL", "https://mainnet.sorobanrpc.com")
	t.Setenv("STELLAR_HORIZON_URL", "https://horizon.stellar.org")
	t.Setenv("STELLAR_USDC_ISSUER", mainnetUSDCIssuer)
	t.Setenv("YIELD_REGISTRY_CONTRACT", "CBYIELDREGISTRY")
	t.Setenv("STELLAR_ALLOCATION_STRATEGY_ADDRESS", "CBALLOCATIONSTRATEGY")
	t.Setenv("STELLAR_ADMIN_MULTISIG_ADDRESS", testMultisigAddress)
}

func expectLoadError(t *testing.T, want string) {
	t.Helper()
	_, err := Load()
	if err == nil {
		t.Fatalf("Load() succeeded, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want it to contain %q", err, want)
	}
}

func TestMainnetProfileLoads(t *testing.T) {
	mainnetEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Stellar().IsMainnet() {
		t.Fatalf("expected mainnet, got %q", cfg.Stellar().Network())
	}
	if cfg.Stellar().AdminMultisigAddress() != testMultisigAddress {
		t.Fatalf("unexpected multisig address %q", cfg.Stellar().AdminMultisigAddress())
	}
}

func TestMainnetRejectsTestnetPassphrase(t *testing.T) {
	mainnetEnv(t)
	t.Setenv("STELLAR_NETWORK_PASSPHRASE", TestnetPassphrase)
	expectLoadError(t, "STELLAR_NETWORK=mainnet but STELLAR_NETWORK_PASSPHRASE is the testnet passphrase")
}

func TestTestnetRejectsMainnetPassphrase(t *testing.T) {
	baseEnv(t)
	requiredEnv(t)
	t.Setenv("STELLAR_NETWORK", NetworkTestnet)
	t.Setenv("STELLAR_NETWORK_PASSPHRASE", MainnetPassphrase)
	expectLoadError(t, "STELLAR_NETWORK=testnet but STELLAR_NETWORK_PASSPHRASE is the mainnet passphrase")
}

func TestMainnetRejectsTestnetEndpoints(t *testing.T) {
	cases := []struct {
		key, value string
	}{
		{"STELLAR_RPC_URL", "https://soroban-testnet.stellar.org"},
		{"STELLAR_HORIZON_URL", "https://horizon-testnet.stellar.org"},
		{"STELLAR_RPC_URL", "https://rpc.ankr.com/stellar_testnet_soroban/key"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			mainnetEnv(t)
			t.Setenv(tc.key, tc.value)
			expectLoadError(t, tc.key+" points at testnet but the configured network is mainnet")
		})
	}
}

func TestTestnetRejectsMainnetEndpoints(t *testing.T) {
	cases := []struct {
		key, value string
	}{
		{"STELLAR_HORIZON_URL", "https://horizon.stellar.org"},
		{"STELLAR_RPC_URL", "https://mainnet.sorobanrpc.com"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			baseEnv(t)
			requiredEnv(t)
			t.Setenv(tc.key, tc.value)
			expectLoadError(t, tc.key+" points at mainnet but the configured network is testnet")
		})
	}
}

// A well-known passphrase implies the network even without STELLAR_NETWORK,
// so the endpoint cross-check still applies in development.
func TestUndeclaredNetworkInfersFromPassphrase(t *testing.T) {
	baseEnv(t)
	requiredEnv(t)
	t.Setenv("STELLAR_NETWORK", "")
	t.Setenv("STELLAR_NETWORK_PASSPHRASE", TestnetPassphrase)
	t.Setenv("STELLAR_HORIZON_URL", "https://horizon.stellar.org")
	expectLoadError(t, "STELLAR_HORIZON_URL points at mainnet but the configured network is testnet")
}

func TestMainnetPassphraseRequiresExplicitNetwork(t *testing.T) {
	mainnetEnv(t)
	t.Setenv("STELLAR_NETWORK", "")
	expectLoadError(t, "STELLAR_NETWORK=mainnet must be set explicitly")
}

func TestProductionRequiresDeclaredNetwork(t *testing.T) {
	mainnetEnv(t)
	t.Setenv("STELLAR_NETWORK", "")
	t.Setenv("STELLAR_NETWORK_PASSPHRASE", TestnetPassphrase)
	t.Setenv("STELLAR_RPC_URL", "https://soroban-testnet.stellar.org")
	t.Setenv("STELLAR_HORIZON_URL", "https://horizon-testnet.stellar.org")
	expectLoadError(t, "STELLAR_NETWORK is required in production or staging")
}

func TestUnknownNetworkRejected(t *testing.T) {
	baseEnv(t)
	requiredEnv(t)
	t.Setenv("STELLAR_NETWORK", "pubnet")
	expectLoadError(t, "STELLAR_NETWORK must be one of mainnet, testnet, futurenet, standalone")
}

func TestMainnetRequiresProductionEnvironment(t *testing.T) {
	mainnetEnv(t)
	t.Setenv("APP_ENV", "development")
	expectLoadError(t, "mainnet requires APP_ENV=production or APP_ENV=staging")
}

func TestMainnetRefusesDotEnvFile(t *testing.T) {
	mainnetEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "STELLAR_OPERATOR_SECRET=SBTESTNETDEVKEY\n")
	chdir(t, dir)
	expectLoadError(t, "mainnet refuses to read a .env file")
}

func TestMainnetUSDCIssuer(t *testing.T) {
	t.Run("must be explicit", func(t *testing.T) {
		mainnetEnv(t)
		t.Setenv("STELLAR_USDC_ISSUER", "")
		expectLoadError(t, "STELLAR_USDC_ISSUER must be set explicitly on mainnet")
	})
	t.Run("rejects testnet issuer", func(t *testing.T) {
		mainnetEnv(t)
		t.Setenv("STELLAR_USDC_ISSUER", testnetUSDCIssuer)
		expectLoadError(t, "STELLAR_USDC_ISSUER is the testnet USDC issuer")
	})
}

func TestMainnetRequiresContractAddresses(t *testing.T) {
	for _, key := range []string{"YIELD_REGISTRY_CONTRACT", "STELLAR_ALLOCATION_STRATEGY_ADDRESS"} {
		t.Run(key, func(t *testing.T) {
			mainnetEnv(t)
			t.Setenv(key, "")
			expectLoadError(t, key+" must be set on mainnet")
		})
	}
}

func TestMainnetAdminMultisig(t *testing.T) {
	t.Run("required", func(t *testing.T) {
		mainnetEnv(t)
		t.Setenv("STELLAR_ADMIN_MULTISIG_ADDRESS", "")
		expectLoadError(t, "STELLAR_ADMIN_MULTISIG_ADDRESS must be set on mainnet")
	})
	t.Run("rejects a G account", func(t *testing.T) {
		mainnetEnv(t)
		t.Setenv("STELLAR_ADMIN_MULTISIG_ADDRESS", mainnetUSDCIssuer)
		expectLoadError(t, "STELLAR_ADMIN_MULTISIG_ADDRESS must be a contract address")
	})
}

func TestNetworkForURL(t *testing.T) {
	cases := map[string]string{
		"https://soroban-testnet.stellar.org":     NetworkTestnet,
		"https://horizon-testnet.stellar.org":     NetworkTestnet,
		"https://rpc-futurenet.stellar.org":       NetworkFuturenet,
		"https://horizon.stellar.org":             NetworkMainnet,
		"https://mainnet.sorobanrpc.com":          NetworkMainnet,
		"https://horizon.pubnet.example.com":      NetworkMainnet,
		"https://rpc.example.com":                 "",
		"http://localhost:8000/soroban/rpc":       "",
		"https://soroban.nester.finance/api/v1/x": "",
	}
	for raw, want := range cases {
		got, _ := networkForURL(raw)
		if got != want {
			t.Errorf("networkForURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
