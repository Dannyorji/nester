#![cfg(test)]

extern crate std;

use soroban_sdk::{
    testutils::{Address as _, Ledger},
    Address, Bytes, BytesN, Env,
};

use nester_access_control::Role;
use nester_common::{MIN_CONTRACT_UPDATE_DELAY, MIN_UPGRADE_DELAY_NESTER};

use crate::{ContractKind, NesterContract, NesterContractClient, ProtocolInitConfig};

// Authorization test matrix (U = unauthorized/unsigned, A = authorized,
// R = role revoked before a subsequent call):
//
// initialize
//   U: initialize_requires_admin_signature
//   A: initialize_stores_all_addresses                     R: N/A (one-shot)
// propose_upgrade / cancel_upgrade (Upgrader)
//   U: admin_without_upgrader_role_cannot_propose_upgrade; privileged_calls_require_signatures
//   A: upgrade_executes_only_after_delay; upgrader_can_cancel_pending_upgrade
//   R: revoked_upgrader_cannot_propose_upgrade
// execute_upgrade (permissionless after ETA)
//   A: upgrade_executes_only_after_delay
// propose_update_contract / cancel_update_contract (Admin)
//   U: non_admin_cannot_propose_contract_update; privileged_calls_require_signatures
//   A: contract_update_is_not_live_until_delay
//   R: revoked_admin_cannot_call_admin_operations
// grant_role
//   U: non_admin_cannot_grant_roles; privileged_calls_require_signatures
//   A: admin_can_grant_and_revoke_operator_role
//   R: revoked_admin_cannot_call_admin_operations
// revoke_role
//   U: non_admin_cannot_revoke_roles; privileged_calls_require_signatures
//   A: admin_can_grant_and_revoke_operator_role
//   R: revoked_admin_cannot_call_admin_operations
// transfer_admin
//   U: non_admin_cannot_transfer_admin; privileged_calls_require_signatures
//   A/R: two_step_admin_transfer
// accept_admin
//   U: wrong_address_cannot_accept_admin; privileged_calls_require_signatures
//   A: two_step_admin_transfer                              R: N/A (address-bound)
//
// Every role-negative case uses otherwise-valid arguments. This is deliberate:
// deleting the corresponding authorization check must make the test succeed and
// therefore fail, rather than merely exposing a later validation panic.

// ── Helpers ───────────────────────────────────────────────────────────────────

#[derive(Clone)]
struct ProtocolAddresses {
    vault_usdc: Address,
    vault_xlm: Address,
    vault_token_usdc: Address,
    vault_token_xlm: Address,
    treasury: Address,
    yield_registry: Address,
    allocation_strategy: Address,
}

impl From<ProtocolAddresses> for ProtocolInitConfig {
    fn from(p: ProtocolAddresses) -> Self {
        ProtocolInitConfig {
            vault_usdc: p.vault_usdc,
            vault_xlm: p.vault_xlm,
            vault_token_usdc: p.vault_token_usdc,
            vault_token_xlm: p.vault_token_xlm,
            treasury: p.treasury,
            yield_registry: p.yield_registry,
            allocation_strategy: p.allocation_strategy,
        }
    }
}

fn fake_protocol(env: &Env) -> ProtocolAddresses {
    ProtocolAddresses {
        vault_usdc: Address::generate(env),
        vault_xlm: Address::generate(env),
        vault_token_usdc: Address::generate(env),
        vault_token_xlm: Address::generate(env),
        treasury: Address::generate(env),
        yield_registry: Address::generate(env),
        allocation_strategy: Address::generate(env),
    }
}

fn setup(env: &Env) -> (NesterContractClient<'_>, Address, ProtocolAddresses) {
    env.mock_all_auths();
    let admin = Address::generate(env);
    let p = fake_protocol(env);

    let id = env.register_contract(None, NesterContract);
    let client = NesterContractClient::new(env, &id);

    client.initialize(&admin, &p.clone().into());

    (client, admin, p)
}

fn advance(env: &Env, seconds: u64) {
    env.ledger().with_mut(|l| l.timestamp += seconds);
}

/// Propose a contract reference change and let it mature.
fn update_contract(
    env: &Env,
    client: &NesterContractClient,
    admin: &Address,
    kind: &ContractKind,
    addr: &Address,
) {
    client.propose_update_contract(admin, kind, addr);
    advance(env, MIN_CONTRACT_UPDATE_DELAY);
    client.execute_update_contract(admin, kind);
}

fn uploaded_test_wasm_hash(env: &Env) -> BytesN<32> {
    // Soroban testutils accepts an empty Wasm blob for native-contract upgrade
    // lifecycle tests. This gives `upgrade` a ledger-backed hash without adding
    // a generated Wasm artifact to the repository.
    env.deployer().upload_contract_wasm(Bytes::new(env))
}

// ── Initialization ────────────────────────────────────────────────────────────

#[test]
fn initialize_stores_all_addresses() {
    let env = Env::default();
    let (client, _, p) = setup(&env);

    assert_eq!(client.vault_usdc(), p.vault_usdc);
    assert_eq!(client.vault_xlm(), p.vault_xlm);
    assert_eq!(client.vault_token_usdc(), p.vault_token_usdc);
    assert_eq!(client.vault_token_xlm(), p.vault_token_xlm);
    assert_eq!(client.treasury(), p.treasury);
    assert_eq!(client.yield_registry(), p.yield_registry);
    assert_eq!(client.allocation_strategy(), p.allocation_strategy);
}

#[test]
fn initialize_sets_version_to_one() {
    let env = Env::default();
    let (client, _, _) = setup(&env);
    assert_eq!(client.version(), 1);
}

#[test]
fn initialize_grants_admin_role_to_deployer() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    assert!(client.has_role(&admin, &Role::Admin));
}

#[test]
#[should_panic]
fn initialize_twice_panics() {
    let env = Env::default();
    let (client, admin, p) = setup(&env);
    // Second call must panic with AlreadyInitialized.
    client.initialize(&admin, &p.clone().into());
}

#[test]
fn initialize_requires_admin_signature() {
    let env = Env::default();
    env.mock_auths(&[]);

    let admin = Address::generate(&env);
    let p = fake_protocol(&env);
    let id = env.register_contract(None, NesterContract);
    let client = NesterContractClient::new(&env, &id);

    assert!(client.try_initialize(&admin, &p.clone().into()).is_err());
    assert_eq!(
        client.version(),
        0,
        "failed initialization must not write state"
    );
}

// ── version before initialization ─────────────────────────────────────────────

#[test]
fn version_returns_zero_before_initialize() {
    let env = Env::default();
    env.mock_all_auths();
    let id = env.register_contract(None, NesterContract);
    let client = NesterContractClient::new(&env, &id);
    assert_eq!(client.version(), 0);
}

// ── Getters panic before initialization ───────────────────────────────────────

#[test]
#[should_panic]
fn vault_usdc_panics_before_initialize() {
    let env = Env::default();
    env.mock_all_auths();
    let id = env.register_contract(None, NesterContract);
    let client = NesterContractClient::new(&env, &id);
    client.vault_usdc();
}

#[test]
#[should_panic]
fn treasury_panics_before_initialize() {
    let env = Env::default();
    env.mock_all_auths();
    let id = env.register_contract(None, NesterContract);
    let client = NesterContractClient::new(&env, &id);
    client.treasury();
}

// -- upgrade (timelocked) -------------------------------------------------

#[test]
fn upgrade_executes_only_after_delay() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let upgrader = Address::generate(&env);
    client.grant_role(&admin, &upgrader, &Role::Upgrader);
    let wasm_hash = uploaded_test_wasm_hash(&env);

    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;
    client.propose_upgrade(&upgrader, &wasm_hash, &eta);

    let pending = client.get_pending_upgrade().unwrap();
    assert_eq!(pending.wasm_hash, wasm_hash);
    assert_eq!(pending.eta, eta);

    // One second short of the public window: still locked.
    advance(&env, MIN_UPGRADE_DELAY_NESTER - 1);
    let anyone = Address::generate(&env);
    assert!(client.try_execute_upgrade(&anyone, &wasm_hash).is_err());
    assert_eq!(client.version(), 1);

    advance(&env, 1);
    client.execute_upgrade(&anyone, &wasm_hash);
    assert_eq!(client.version(), 2);
    assert!(client.get_pending_upgrade().is_none());
}

#[test]
fn propose_upgrade_rejects_eta_inside_delay() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    client.grant_role(&admin, &admin, &Role::Upgrader);
    let wasm_hash = uploaded_test_wasm_hash(&env);

    let too_soon = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER - 1;
    assert!(client
        .try_propose_upgrade(&admin, &wasm_hash, &too_soon)
        .is_err());
    assert!(client.get_pending_upgrade().is_none());
}

#[test]
fn execute_upgrade_rejects_mismatched_hash() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    client.grant_role(&admin, &admin, &Role::Upgrader);
    let wasm_hash = uploaded_test_wasm_hash(&env);
    let other = BytesN::from_array(&env, &[7u8; 32]);

    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;
    client.propose_upgrade(&admin, &wasm_hash, &eta);
    advance(&env, MIN_UPGRADE_DELAY_NESTER);

    assert!(client.try_execute_upgrade(&admin, &other).is_err());
    assert_eq!(client.version(), 1);
}

#[test]
fn upgrader_can_cancel_pending_upgrade() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    client.grant_role(&admin, &admin, &Role::Upgrader);
    let wasm_hash = uploaded_test_wasm_hash(&env);

    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;
    client.propose_upgrade(&admin, &wasm_hash, &eta);
    client.cancel_upgrade(&admin);
    assert!(client.get_pending_upgrade().is_none());

    advance(&env, MIN_UPGRADE_DELAY_NESTER);
    assert!(client.try_execute_upgrade(&admin, &wasm_hash).is_err());
    assert_eq!(client.version(), 1);
}

#[test]
fn admin_without_upgrader_role_cannot_propose_upgrade() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let wasm_hash = uploaded_test_wasm_hash(&env);
    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;

    assert!(client
        .try_propose_upgrade(&admin, &wasm_hash, &eta)
        .is_err());
    assert!(client.get_pending_upgrade().is_none());
}

#[test]
fn revoked_upgrader_cannot_propose_upgrade() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let upgrader = Address::generate(&env);
    client.grant_role(&admin, &upgrader, &Role::Upgrader);
    client.revoke_role(&admin, &upgrader, &Role::Upgrader);
    let wasm_hash = uploaded_test_wasm_hash(&env);
    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;

    assert!(client
        .try_propose_upgrade(&upgrader, &wasm_hash, &eta)
        .is_err());
}

// ── update_contract (timelocked) ──────────────────────────────────────────────

#[test]
fn contract_update_is_not_live_until_delay() {
    let env = Env::default();
    let (client, admin, p) = setup(&env);
    let new_vault = Address::generate(&env);

    let eta = client.propose_update_contract(&admin, &ContractKind::VaultUsdc, &new_vault);
    assert_eq!(eta, env.ledger().timestamp() + MIN_CONTRACT_UPDATE_DELAY);
    let pending = client.get_pending_update(&ContractKind::VaultUsdc).unwrap();
    assert_eq!(pending.new_address, new_vault);

    // Proposed but not yet live: getters still return the old address.
    assert_eq!(client.vault_usdc(), p.vault_usdc);
    advance(&env, MIN_CONTRACT_UPDATE_DELAY - 1);
    assert!(client
        .try_execute_update_contract(&admin, &ContractKind::VaultUsdc)
        .is_err());
    assert_eq!(client.vault_usdc(), p.vault_usdc);

    advance(&env, 1);
    let anyone = Address::generate(&env);
    client.execute_update_contract(&anyone, &ContractKind::VaultUsdc);
    assert_eq!(client.vault_usdc(), new_vault);
    assert!(client
        .get_pending_update(&ContractKind::VaultUsdc)
        .is_none());
}

#[test]
fn admin_can_cancel_pending_contract_update() {
    let env = Env::default();
    let (client, admin, p) = setup(&env);

    client.propose_update_contract(&admin, &ContractKind::Treasury, &Address::generate(&env));
    client.cancel_update_contract(&admin, &ContractKind::Treasury);
    advance(&env, MIN_CONTRACT_UPDATE_DELAY);

    assert!(client
        .try_execute_update_contract(&admin, &ContractKind::Treasury)
        .is_err());
    assert_eq!(client.treasury(), p.treasury);
}

#[test]
fn update_contract_does_not_affect_other_references() {
    let env = Env::default();
    let (client, admin, p) = setup(&env);
    let new_vault_xlm = Address::generate(&env);

    update_contract(
        &env,
        &client,
        &admin,
        &ContractKind::VaultXlm,
        &new_vault_xlm,
    );

    // Only VaultXlm changed; everything else is unchanged.
    assert_eq!(client.vault_xlm(), new_vault_xlm);
    assert_eq!(client.vault_usdc(), p.vault_usdc);
    assert_eq!(client.treasury(), p.treasury);
    assert_eq!(client.yield_registry(), p.yield_registry);
    assert_eq!(client.allocation_strategy(), p.allocation_strategy);
}

#[test]
fn update_contract_covers_all_kinds() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);

    let kinds = [
        ContractKind::VaultUsdc,
        ContractKind::VaultXlm,
        ContractKind::VaultTokenUsdc,
        ContractKind::VaultTokenXlm,
        ContractKind::Treasury,
        ContractKind::YieldRegistry,
        ContractKind::AllocationStrategy,
    ];

    for kind in kinds {
        let new_addr = Address::generate(&env);
        update_contract(&env, &client, &admin, &kind, &new_addr);
        // Confirm each getter now returns the updated address.
        let actual = match kind {
            ContractKind::VaultUsdc => client.vault_usdc(),
            ContractKind::VaultXlm => client.vault_xlm(),
            ContractKind::VaultTokenUsdc => client.vault_token_usdc(),
            ContractKind::VaultTokenXlm => client.vault_token_xlm(),
            ContractKind::Treasury => client.treasury(),
            ContractKind::YieldRegistry => client.yield_registry(),
            ContractKind::AllocationStrategy => client.allocation_strategy(),
        };
        assert_eq!(actual, new_addr);
    }
}

#[test]
fn non_admin_cannot_propose_contract_update() {
    let env = Env::default();
    let (client, _, _) = setup(&env);
    let outsider = Address::generate(&env);

    assert!(client
        .try_propose_update_contract(&outsider, &ContractKind::Treasury, &Address::generate(&env))
        .is_err());
    assert!(client.get_pending_update(&ContractKind::Treasury).is_none());
}

// ── Access control ────────────────────────────────────────────────────────────

#[test]
fn admin_can_grant_and_revoke_operator_role() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let operator = Address::generate(&env);

    client.grant_role(&admin, &operator, &Role::Operator);
    assert!(client.has_role(&operator, &Role::Operator));

    client.revoke_role(&admin, &operator, &Role::Operator);
    assert!(!client.has_role(&operator, &Role::Operator));
}

#[test]
#[should_panic]
fn non_admin_cannot_grant_roles() {
    let env = Env::default();
    let (client, _, _) = setup(&env);
    let outsider = Address::generate(&env);

    client.grant_role(&outsider, &Address::generate(&env), &Role::Operator);
}

#[test]
fn has_role_returns_false_for_unknown_account() {
    let env = Env::default();
    let (client, _, _) = setup(&env);
    let stranger = Address::generate(&env);
    assert!(!client.has_role(&stranger, &Role::Admin));
    assert!(!client.has_role(&stranger, &Role::Operator));
}

#[test]
fn two_step_admin_transfer() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let new_admin = Address::generate(&env);

    client.transfer_admin(&admin, &new_admin);
    client.accept_admin(&new_admin);

    assert!(client.has_role(&new_admin, &Role::Admin));
    assert!(!client.has_role(&admin, &Role::Admin));

    let replacement = Address::generate(&env);
    update_contract(
        &env,
        &client,
        &new_admin,
        &ContractKind::Treasury,
        &replacement,
    );
    assert_eq!(client.treasury(), replacement);
    assert!(client
        .try_propose_update_contract(&admin, &ContractKind::Treasury, &Address::generate(&env),)
        .is_err());
}

#[test]
fn non_admin_cannot_revoke_roles() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let outsider = Address::generate(&env);
    let operator = Address::generate(&env);
    client.grant_role(&admin, &operator, &Role::Operator);

    assert!(client
        .try_revoke_role(&outsider, &operator, &Role::Operator)
        .is_err());
    assert!(client.has_role(&operator, &Role::Operator));
}

#[test]
fn non_admin_cannot_transfer_admin() {
    let env = Env::default();
    let (client, _, _) = setup(&env);
    let outsider = Address::generate(&env);

    assert!(client
        .try_transfer_admin(&outsider, &Address::generate(&env))
        .is_err());
}

#[test]
fn wrong_address_cannot_accept_admin() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let proposed_admin = Address::generate(&env);
    let outsider = Address::generate(&env);
    client.transfer_admin(&admin, &proposed_admin);

    assert!(client.try_accept_admin(&outsider).is_err());
    assert!(client.has_role(&admin, &Role::Admin));
    assert!(!client.has_role(&outsider, &Role::Admin));
}

#[test]
fn revoked_admin_cannot_call_admin_operations() {
    let env = Env::default();
    let (client, initial_admin, _) = setup(&env);
    let remaining_admin = Address::generate(&env);
    let operator = Address::generate(&env);
    client.grant_role(&initial_admin, &remaining_admin, &Role::Admin);
    client.grant_role(&initial_admin, &operator, &Role::Operator);
    client.revoke_role(&remaining_admin, &initial_admin, &Role::Admin);

    assert!(client
        .try_propose_update_contract(
            &initial_admin,
            &ContractKind::Treasury,
            &Address::generate(&env),
        )
        .is_err());
    assert!(client
        .try_grant_role(&initial_admin, &Address::generate(&env), &Role::Operator,)
        .is_err());
    assert!(client
        .try_revoke_role(&initial_admin, &operator, &Role::Operator)
        .is_err());
    assert!(client
        .try_transfer_admin(&initial_admin, &Address::generate(&env))
        .is_err());
}

#[test]
fn privileged_calls_require_signatures() {
    let env = Env::default();
    let (client, admin, _) = setup(&env);
    let operator = Address::generate(&env);
    let proposed_admin = Address::generate(&env);
    let wasm_hash = uploaded_test_wasm_hash(&env);
    let eta = env.ledger().timestamp() + MIN_UPGRADE_DELAY_NESTER;
    client.grant_role(&admin, &operator, &Role::Operator);
    client.grant_role(&admin, &admin, &Role::Upgrader);
    client.transfer_admin(&admin, &proposed_admin);

    env.mock_auths(&[]);

    assert!(client
        .try_propose_update_contract(&admin, &ContractKind::Treasury, &Address::generate(&env),)
        .is_err());
    assert!(client
        .try_grant_role(&admin, &Address::generate(&env), &Role::Operator)
        .is_err());
    assert!(client
        .try_revoke_role(&admin, &operator, &Role::Operator)
        .is_err());
    assert!(client
        .try_transfer_admin(&admin, &Address::generate(&env))
        .is_err());
    assert!(client.try_accept_admin(&proposed_admin).is_err());
    assert!(client
        .try_propose_upgrade(&admin, &wasm_hash, &eta)
        .is_err());
    assert!(client.try_cancel_upgrade(&admin).is_err());
}
