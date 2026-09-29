package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Stellar network profiles (#1371). STELLAR_NETWORK names the network a
// deployment is meant to talk to; the loader then refuses to boot when the
// passphrase, endpoints or credentials belong to a different network, so a
// mainnet process can never silently run against testnet (or vice versa).
const (
	NetworkMainnet    = "mainnet"
	NetworkTestnet    = "testnet"
	NetworkFuturenet  = "futurenet"
	NetworkStandalone = "standalone"
)

// Canonical network passphrases published by the Stellar Development Foundation.
const (
	MainnetPassphrase    = "Public Global Stellar Network ; September 2015"
	TestnetPassphrase    = "Test SDF Network ; September 2015"
	FuturenetPassphrase  = "Test SDF Future Network ; October 2022"
	StandalonePassphrase = "Standalone Network ; February 2017"
)

// testnetUSDCIssuer is Circle's testnet USDC issuer. A mainnet deployment that
// is configured with it would price and settle against a worthless asset.
const testnetUSDCIssuer = "GBBD47IF6LWK7P7MDEVSCWR7DPUWV3NY3DTQEVFL4NAT4AQH3ZLLFLA5"

var networkPassphrases = map[string]string{
	NetworkMainnet:    MainnetPassphrase,
	NetworkTestnet:    TestnetPassphrase,
	NetworkFuturenet:  FuturenetPassphrase,
	NetworkStandalone: StandalonePassphrase,
}

// PassphraseForNetwork returns the canonical passphrase for a network name.
func PassphraseForNetwork(network string) (string, bool) {
	p, ok := networkPassphrases[network]
	return p, ok
}

// NetworkForPassphrase maps a known passphrase back to its network name.
// Unknown passphrases (private networks, test fixtures) report ok=false.
func NetworkForPassphrase(passphrase string) (string, bool) {
	for network, p := range networkPassphrases {
		if p == passphrase {
			return network, true
		}
	}
	return "", false
}

// networkForURL infers the network an endpoint serves from its host and path.
// Only unambiguous markers are recognised; custom or self-hosted endpoints
// report ok=false and are verified against the live passphrase at startup
// instead (see stellar.FetchHorizonPassphrase / FetchRPCPassphrase).
func networkForURL(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	target := host + strings.ToLower(parsed.Path)

	switch {
	case strings.Contains(target, "futurenet"):
		return NetworkFuturenet, true
	case strings.Contains(target, "testnet"):
		return NetworkTestnet, true
	case host == "horizon.stellar.org",
		strings.Contains(target, "mainnet"),
		strings.Contains(target, "pubnet"):
		return NetworkMainnet, true
	}
	return "", false
}

// validateStellarNetwork enforces the network profile rules: the declared
// network, passphrase and endpoints must all agree.
func (c *Config) validateStellarNetwork(loader *envLoader) {
	declared := c.stellar.network
	passphrase := c.stellar.networkPassphrase
	production := c.environment == "production" || c.environment == "staging"

	if declared == "" {
		if production {
			loader.addError("STELLAR_NETWORK is required in production or staging (one of mainnet, testnet, futurenet, standalone)")
		}
	} else if expected, ok := PassphraseForNetwork(declared); !ok {
		loader.addError("STELLAR_NETWORK must be one of mainnet, testnet, futurenet, standalone")
		return
	} else if passphrase != "" && passphrase != expected {
		actual := "an unrecognised passphrase"
		if n, known := NetworkForPassphrase(passphrase); known {
			actual = "the " + n + " passphrase"
		}
		loader.addError(fmt.Sprintf("STELLAR_NETWORK=%s but STELLAR_NETWORK_PASSPHRASE is %s", declared, actual))
	}

	// Without an explicit STELLAR_NETWORK, a well-known passphrase still
	// tells us which network the endpoints must belong to.
	effective := declared
	if effective == "" {
		effective, _ = NetworkForPassphrase(passphrase)
	}
	if effective == "" {
		return
	}

	for _, ep := range []struct{ key, value string }{
		{"STELLAR_RPC_URL", c.stellar.rpcURL},
		{"STELLAR_HORIZON_URL", c.stellar.horizonURL},
	} {
		if n, known := networkForURL(ep.value); known && n != effective {
			loader.addError(fmt.Sprintf("%s points at %s but the configured network is %s", ep.key, n, effective))
		}
	}

	if effective == NetworkMainnet {
		c.validateMainnetProfile(loader, declared)
	}
}

// validateMainnetProfile applies the extra isolation rules for mainnet.
func (c *Config) validateMainnetProfile(loader *envLoader, declared string) {
	if declared != NetworkMainnet {
		loader.addError("STELLAR_NETWORK=mainnet must be set explicitly when using the mainnet passphrase")
	}

	if c.environment != "production" && c.environment != "staging" {
		loader.addError("mainnet requires APP_ENV=production or APP_ENV=staging")
	}

	// A developer's .env holds testnet keys and dev defaults. Mainnet config
	// must come from the deployment's secret store via the process
	// environment, never from a file that may have been copied between hosts.
	if len(loader.fileValues) > 0 {
		loader.addError("mainnet refuses to read a .env file; supply configuration through the process environment")
	}

	if issuer, ok := loader.lookup("STELLAR_USDC_ISSUER"); !ok {
		loader.addError("STELLAR_USDC_ISSUER must be set explicitly on mainnet")
	} else if issuer == testnetUSDCIssuer {
		loader.addError("STELLAR_USDC_ISSUER is the testnet USDC issuer; set the mainnet issuer")
	}

	if strings.TrimSpace(c.stellar.yieldRegistryContract) == "" {
		loader.addError("YIELD_REGISTRY_CONTRACT must be set on mainnet")
	}
	if strings.TrimSpace(c.stellar.allocationStrategyAddress) == "" {
		loader.addError("STELLAR_ALLOCATION_STRATEGY_ADDRESS must be set on mainnet")
	}

	// #1374: privileged roles on mainnet are held by an N-of-M multisig
	// account contract, never by the API's single operator key.
	multisig := c.stellar.adminMultisigAddress
	if multisig == "" {
		loader.addError("STELLAR_ADMIN_MULTISIG_ADDRESS must be set on mainnet")
	} else if !isContractStrkey(multisig) {
		loader.addError("STELLAR_ADMIN_MULTISIG_ADDRESS must be a contract address (C..., 56 characters)")
	}
}

// isContractStrkey is a shape check for a Soroban contract address. Full
// checksum verification is left to the SDK; this only catches a G-account or
// a truncated value being pasted where the multisig contract belongs.
func isContractStrkey(s string) bool {
	if len(s) != 56 || s[0] != 'C' {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}
