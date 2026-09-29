# Mainnet Admin Multisig

On mainnet, no single key can pause, upgrade, change caps or fees, or move
treasury funds. Those roles belong to **N-of-M multisig account contracts**
([`contracts/multisig`](../../packages/contracts/contracts/multisig/src/lib.rs)),
not to people or servers. Tracked in #1374.

## How it works

The multisig is a Soroban **custom account**. Its `C...` address is granted
roles like any other address. When a protocol contract calls
`caller.require_auth()` on it, Soroban runs the multisig's `__check_auth`,
which accepts the call only with valid ed25519 signatures from at least
`threshold` distinct registered signers.

The existing role checks (`AccessControl::require_role`) in the vault,
treasury, registry and so on don't change at all. Holding the role *is* the
multisig requirement.

| Property | Rule |
|---|---|
| Threshold | `2 <= threshold <= signers`. A 1-of-M "multisig" is rejected. |
| Signers | 1–20 unique ed25519 public keys |
| Signature order | Strictly ascending by public key, which also rules out duplicates |
| Signer/threshold changes | `add_signer`, `remove_signer` and `set_threshold` need the account's own threshold approval |
| Lock-out protection | Removing a signer can't leave fewer signers than the threshold |

## Two accounts, two thresholds

| Account | Suggested policy | Holds | Why |
|---|---|---|---|
| **Admin multisig** | 3-of-5, hardware keys, geographically split | `Admin`, `Upgrader`, `Treasurer`, `FeeManager` on every contract | Everything that can move funds, change code, or loosen limits |
| **Guardian multisig** | 2-of-7, wider on-call pool | `Guardian` on every vault | An emergency halt has to be quick. A Guardian can only make things *safer* (see [SECURITY.md](../../SECURITY.md#on-chain-access-control-model-issue-820)), so a lower threshold is an acceptable trade. |

Operational roles (`Operator`, `RebalanceKeeper`, `Attester`) stay with
service keys, because they can't move funds or change code.

Every upgrade also waits out a public timelock (48h, or 7 days for the
treasury; see #1373). So even a quorum of compromised Admin signers can't ship
new code before users have had the chance to withdraw.

## Role handoff at launch

Do this per contract, **before** the contracts are announced or the frontend
points at them.

1. **Deploy and initialise the multisigs.** Build `nester_multisig.wasm`,
   deploy it twice, and call
   `initialize(signers: Vec<BytesN<32>>, threshold: u32)` on each. A signer's
   public key is the raw 32 bytes behind its `G...` address:
   `stellar keys address <id>`, then strkey-decode it.
2. **Grant the roles.** From the deployer key:
   `grant_role(deployer, ADMIN_MULTISIG, Admin)`, then `Upgrader`,
   `Treasurer` and `FeeManager` where the contract uses them, and
   `grant_role(deployer, GUARDIAN_MULTISIG, Guardian)` on each vault.
3. **Prove the multisig works before relying on it.** Using the multisig
   (threshold signatures), revoke a throwaway role from a test address. If
   this fails, stop here: the deployer still holds Admin.
4. **Revoke the deployer.** Using the multisig:
   `revoke_role(ADMIN_MULTISIG, deployer, Admin)`, and likewise for
   `Upgrader`, `Treasurer` and `Guardian`. Last-admin protection means this
   only succeeds once the multisig already holds Admin.
5. **Verify on-chain:**
   ```bash
   ADMIN_MULTISIG=C... GUARDIAN_MULTISIG=C... DEPLOYER=G... OPERATOR=G... \
   SOURCE_ACCOUNT=<any funded identity> \
   bash packages/contracts/scripts/verify-mainnet-roles.sh scripts/deployed-mainnet.env
   ```
   This must exit 0. Keep the output with the launch record, and re-run it
   after every role change.
6. **Configure the API** with `STELLAR_ADMIN_MULTISIG_ADDRESS=<ADMIN_MULTISIG>`.
   The API won't boot on mainnet without it (#1371).

## What changes in the admin API

On mainnet, the admin API's pause and unpause endpoints
(`POST /api/v1/admin/vaults/{id}/pause|unpause`) return
`409 MULTISIG_REQUIRED` instead of signing with the API's operator key. The
vault's status in the database only changes after the on-chain call
succeeds, so a refused call leaves nothing inconsistent. Emergency halts go
through the Guardian multisig; see the
[incident response runbook](incident-response.md#3-emergency-halt).

Rebalances and allocation-weight updates are operator-level roles and keep
working through the API.

## Signing a multisig transaction

Each signer signs the 32-byte **authorization payload**: the SHA-256 of the
`HashIdPreimage::SorobanAuthorization` for the invocation (network ID, nonce,
expiration ledger and invocation tree). The coordinator then submits one
`Vec<Signature>`, sorted by public key, as the auth entry's signature.
`signed_entry` in
[`contracts/multisig/src/test.rs`](../../packages/contracts/contracts/multisig/src/test.rs)
is a working reference for building the payload.

> **Not built yet:** a CLI helper that builds the payload from a simulated
> transaction, collects signatures offline, and assembles the auth entry.
> Until it exists, signing is a manual step. Tooling and a rehearsal on
> testnet are launch blockers for #1374.

## Rotating a signer

1. The Admin multisig approves `add_signer(new_key)`.
2. The Admin multisig approves `remove_signer(old_key)`. Use the new key as
   one of the signers if possible, to prove it works.
3. Re-run `verify-mainnet-roles.sh`. Role holdings don't change, since roles
   belong to the multisig address, not to its signers.

If a signer's key is suspected compromised, rotate it immediately. Treat it
as a Sev-2 incident under the [incident response runbook](incident-response.md).
