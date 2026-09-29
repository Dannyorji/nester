//! Nester protocol orchestrator contract.
//!
//! This is the single on-chain entry point for the entire Nester protocol.
//! It holds the canonical addresses of every deployed protocol contract
//! (vaults, vault tokens, treasury, yield registry, allocation strategy)
//! and exposes them as read-only getters so frontends and off-chain services
//! never need to hardcode individual contract IDs.
//!
//! # Roles
//! Uses the shared `nester_access_control` library; the deployer is granted
//! `Role::Admin` during `initialize`.
//!
//! # Upgrade path (timelocked, #1373)
//! Upgrades are never instant. An `Upgrader` calls `propose_upgrade` with a
//! WASM hash and an ETA at least [`MIN_UPGRADE_DELAY_NESTER`] (48 hours) in
//! the future; the pending hash and ETA are public via `get_pending_upgrade`
//! and a `PROP_UPG` event, so users and integrators can react before it lands.
//! After the ETA anyone may call `execute_upgrade` with the same hash, which
//! swaps the WASM in place (keeping the contract ID) and bumps the version
//! counter. An `Upgrader` can `cancel_upgrade` at any time before execution.
//!
//! # Contract references (timelocked, #1373)
//! Frontends and services resolve vault and treasury addresses through this
//! contract, so repointing one is as consequential as an upgrade. Admins
//! `propose_update_contract`; the new address becomes live only after
//! [`MIN_CONTRACT_UPDATE_DELAY`] via `execute_update_contract`.

#![no_std]

use soroban_sdk::{
    contract, contractimpl, contracttype, panic_with_error, symbol_short, Address, BytesN, Env,
    Symbol,
};

use nester_access_control::{AccessControl, Role};
use nester_common::{
    emit_event, ContractError, PendingUpgrade, Upgrade, MIN_CONTRACT_UPDATE_DELAY,
    MIN_UPGRADE_DELAY_NESTER,
};

const NESTER: Symbol = symbol_short!("NESTER");
const INIT: Symbol = symbol_short!("INIT");
const UPGRADED: Symbol = symbol_short!("UPGRADED");
const CTR_UPD: Symbol = symbol_short!("CTR_UPD");
const CTR_PROP: Symbol = symbol_short!("CTR_PROP");
const CTR_CAN: Symbol = symbol_short!("CTR_CAN");

// ── Event payloads ────────────────────────────────────────────────────────────

#[contracttype]
#[derive(Clone, Debug)]
pub struct InitializedEventData {
    pub vault_usdc: Address,
    pub vault_xlm: Address,
    pub vault_token_usdc: Address,
    pub vault_token_xlm: Address,
    pub treasury: Address,
    pub yield_registry: Address,
    pub allocation_strategy: Address,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ContractUpdatedEventData {
    pub kind: ContractKind,
    pub old_address: Address,
    pub new_address: Address,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ContractUpdateProposedEventData {
    pub kind: ContractKind,
    pub new_address: Address,
    pub eta: u64,
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ContractUpdateCancelledEventData {
    pub kind: ContractKind,
    pub new_address: Address,
}

// ── Public types ──────────────────────────────────────────────────────────────

/// A proposed change to one protocol contract reference, visible to everyone
/// for at least [`MIN_CONTRACT_UPDATE_DELAY`] before it can take effect.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PendingContractUpdate {
    pub new_address: Address,
    pub eta: u64,
    pub proposer: Address,
}

/// Identifies one of the protocol contracts tracked by this orchestrator.
///
/// Used as a parameter to [`NesterContract::update_contract`] so callers
/// can update a single reference without touching the others.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum ContractKind {
    VaultUsdc,
    VaultXlm,
    VaultTokenUsdc,
    VaultTokenXlm,
    Treasury,
    YieldRegistry,
    AllocationStrategy,
}

// ── Storage ───────────────────────────────────────────────────────────────────

#[contracttype]
#[derive(Clone)]
enum DataKey {
    Contract(ContractKind),
    /// Monotonically increasing value; starts at 1, incremented by `execute_upgrade`.
    Version,
    /// Pending timelocked change to a contract reference.
    PendingUpdate(ContractKind),
}

// ── Private helpers ───────────────────────────────────────────────────────────

fn get_pending_update(env: &Env, kind: &ContractKind) -> PendingContractUpdate {
    env.storage()
        .instance()
        .get(&DataKey::PendingUpdate(kind.clone()))
        .unwrap_or_else(|| panic_with_error!(env, ContractError::NoPendingUpgrade))
}

fn require_initialized(env: &Env) {
    if !env.storage().instance().has(&DataKey::Version) {
        panic_with_error!(env, ContractError::NotInitialized);
    }
}

fn get_contract(env: &Env, kind: &ContractKind) -> Address {
    env.storage()
        .instance()
        .get::<DataKey, Address>(&DataKey::Contract(kind.clone()))
        .unwrap_or_else(|| panic_with_error!(env, ContractError::NotInitialized))
}

#[contracttype]
#[derive(Clone, Debug)]
pub struct ProtocolInitConfig {
    pub vault_usdc: Address,
    pub vault_xlm: Address,
    pub vault_token_usdc: Address,
    pub vault_token_xlm: Address,
    pub treasury: Address,
    pub yield_registry: Address,
    pub allocation_strategy: Address,
}

// ── Contract ──────────────────────────────────────────────────────────────────

#[contract]
pub struct NesterContract;

#[contractimpl]
impl NesterContract {
    // ── Initialisation ────────────────────────────────────────────────────────

    /// Initialise the orchestrator with the addresses of all deployed protocol
    /// contracts. Can only be called once; a second call panics with
    /// `AlreadyInitialized`.
    pub fn initialize(env: Env, admin: Address, config: ProtocolInitConfig) {
        AccessControl::initialize(&env, &admin);

        let s = env.storage().instance();
        s.set(
            &DataKey::Contract(ContractKind::VaultUsdc),
            &config.vault_usdc,
        );
        s.set(
            &DataKey::Contract(ContractKind::VaultXlm),
            &config.vault_xlm,
        );
        s.set(
            &DataKey::Contract(ContractKind::VaultTokenUsdc),
            &config.vault_token_usdc,
        );
        s.set(
            &DataKey::Contract(ContractKind::VaultTokenXlm),
            &config.vault_token_xlm,
        );
        s.set(&DataKey::Contract(ContractKind::Treasury), &config.treasury);
        s.set(
            &DataKey::Contract(ContractKind::YieldRegistry),
            &config.yield_registry,
        );
        s.set(
            &DataKey::Contract(ContractKind::AllocationStrategy),
            &config.allocation_strategy,
        );
        s.set(&DataKey::Version, &1u32);
        Upgrade::init_schema_version(&env, 1);

        emit_event(
            &env,
            NESTER,
            INIT,
            env.current_contract_address(),
            InitializedEventData {
                vault_usdc: config.vault_usdc,
                vault_xlm: config.vault_xlm,
                vault_token_usdc: config.vault_token_usdc,
                vault_token_xlm: config.vault_token_xlm,
                treasury: config.treasury,
                yield_registry: config.yield_registry,
                allocation_strategy: config.allocation_strategy,
            },
        );
    }

    // ── Upgrade (timelocked) ──────────────────────────────────────────────────

    /// Propose replacing the contract WASM with `new_wasm_hash` at `eta`.
    ///
    /// Requires `Upgrader`. `eta` must be at least [`MIN_UPGRADE_DELAY_NESTER`]
    /// (48 hours) from now. Proposing again replaces the pending upgrade and
    /// restarts the full delay.
    pub fn propose_upgrade(env: Env, admin: Address, new_wasm_hash: BytesN<32>, eta: u64) {
        require_initialized(&env);
        AccessControl::require_role(&env, &admin, Role::Upgrader);
        Upgrade::propose_upgrade(&env, &admin, new_wasm_hash, MIN_UPGRADE_DELAY_NESTER, eta);
    }

    /// Cancel the pending upgrade. Requires `Upgrader`.
    pub fn cancel_upgrade(env: Env, admin: Address) {
        require_initialized(&env);
        AccessControl::require_role(&env, &admin, Role::Upgrader);
        Upgrade::cancel_upgrade(&env, &admin);
    }

    /// Execute a matured upgrade. Permissionless once the ETA has passed;
    /// `wasm_hash` must match the proposed hash.
    ///
    /// The version counter is incremented and the event is published *before*
    /// the WASM swap so that the current ABI encodes both correctly. Any
    /// failure in the swap reverts the whole invocation, including the bump.
    pub fn execute_upgrade(env: Env, caller: Address, wasm_hash: BytesN<32>) {
        require_initialized(&env);

        let version: u32 = env.storage().instance().get(&DataKey::Version).unwrap_or(1);
        let next_version = version + 1;
        env.storage()
            .instance()
            .set(&DataKey::Version, &next_version);
        env.events()
            .publish((NESTER, UPGRADED, caller.clone()), next_version);

        Upgrade::execute_upgrade(&env, &caller, wasm_hash);
    }

    /// The pending upgrade, if any: hash, ETA and proposer.
    pub fn get_pending_upgrade(env: Env) -> Option<PendingUpgrade> {
        Upgrade::get_pending_upgrade(&env)
    }

    // ── Protocol contract registry (timelocked) ───────────────────────────────

    /// Propose repointing one protocol contract reference. Admin-only.
    ///
    /// The change becomes executable after [`MIN_CONTRACT_UPDATE_DELAY`].
    /// Proposing again for the same kind replaces the pending change and
    /// restarts the delay. Returns the ETA.
    pub fn propose_update_contract(
        env: Env,
        admin: Address,
        kind: ContractKind,
        new_address: Address,
    ) -> u64 {
        admin.require_auth();
        require_initialized(&env);
        AccessControl::require_role(&env, &admin, Role::Admin);
        // Reject unknown kinds before queuing anything.
        get_contract(&env, &kind);

        let eta = env
            .ledger()
            .timestamp()
            .saturating_add(MIN_CONTRACT_UPDATE_DELAY);
        env.storage().instance().set(
            &DataKey::PendingUpdate(kind.clone()),
            &PendingContractUpdate {
                new_address: new_address.clone(),
                eta,
                proposer: admin.clone(),
            },
        );

        emit_event(
            &env,
            NESTER,
            CTR_PROP,
            admin,
            ContractUpdateProposedEventData {
                kind,
                new_address,
                eta,
            },
        );
        eta
    }

    /// Cancel a pending contract reference change. Admin-only.
    pub fn cancel_update_contract(env: Env, admin: Address, kind: ContractKind) {
        admin.require_auth();
        require_initialized(&env);
        AccessControl::require_role(&env, &admin, Role::Admin);

        let pending = get_pending_update(&env, &kind);
        env.storage()
            .instance()
            .remove(&DataKey::PendingUpdate(kind.clone()));

        emit_event(
            &env,
            NESTER,
            CTR_CAN,
            admin,
            ContractUpdateCancelledEventData {
                kind,
                new_address: pending.new_address,
            },
        );
    }

    /// Apply a matured contract reference change. Permissionless once the
    /// ETA has passed, mirroring `execute_upgrade`.
    pub fn execute_update_contract(env: Env, caller: Address, kind: ContractKind) {
        caller.require_auth();
        require_initialized(&env);

        let pending = get_pending_update(&env, &kind);
        if env.ledger().timestamp() < pending.eta {
            panic_with_error!(&env, ContractError::UpgradeNotMatured);
        }

        let old_address = get_contract(&env, &kind);
        env.storage()
            .instance()
            .set(&DataKey::Contract(kind.clone()), &pending.new_address);
        env.storage()
            .instance()
            .remove(&DataKey::PendingUpdate(kind.clone()));

        emit_event(
            &env,
            NESTER,
            CTR_UPD,
            caller,
            ContractUpdatedEventData {
                kind,
                old_address,
                new_address: pending.new_address,
            },
        );
    }

    /// The pending change for `kind`, if any.
    pub fn get_pending_update(env: Env, kind: ContractKind) -> Option<PendingContractUpdate> {
        env.storage().instance().get(&DataKey::PendingUpdate(kind))
    }

    // ── Address getters ───────────────────────────────────────────────────────

    pub fn vault_usdc(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::VaultUsdc)
    }

    pub fn vault_xlm(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::VaultXlm)
    }

    pub fn vault_token_usdc(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::VaultTokenUsdc)
    }

    pub fn vault_token_xlm(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::VaultTokenXlm)
    }

    pub fn treasury(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::Treasury)
    }

    pub fn yield_registry(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::YieldRegistry)
    }

    pub fn allocation_strategy(env: Env) -> Address {
        require_initialized(&env);
        get_contract(&env, &ContractKind::AllocationStrategy)
    }

    pub fn version(env: Env) -> u32 {
        env.storage().instance().get(&DataKey::Version).unwrap_or(0)
    }

    // ── Access control ────────────────────────────────────────────────────────

    pub fn grant_role(env: Env, grantor: Address, grantee: Address, role: Role) {
        require_initialized(&env);
        AccessControl::grant_role(&env, &grantor, &grantee, role);
    }

    pub fn revoke_role(env: Env, revoker: Address, target: Address, role: Role) {
        require_initialized(&env);
        AccessControl::revoke_role(&env, &revoker, &target, role);
    }

    pub fn transfer_admin(env: Env, current_admin: Address, new_admin: Address) {
        require_initialized(&env);
        AccessControl::transfer_admin(&env, &current_admin, &new_admin);
    }

    pub fn accept_admin(env: Env, new_admin: Address) {
        require_initialized(&env);
        AccessControl::accept_admin(&env, &new_admin);
    }

    pub fn has_role(env: Env, account: Address, role: Role) -> bool {
        AccessControl::has_role(&env, &account, role)
    }
}

#[cfg(test)]
mod test;
