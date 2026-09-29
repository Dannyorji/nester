//! Nester N-of-M multisig account contract (#1374).
//!
//! A Soroban custom account: its address can hold roles in any Nester
//! contract, and every `require_auth()` on that address is resolved by
//! [`CustomAccountInterface::__check_auth`] here, which demands valid ed25519
//! signatures from at least `threshold` distinct registered signers.
//!
//! On mainnet this contract — not an individual key — holds `Admin`,
//! `Upgrader`, `Guardian` and `Treasurer` on every protocol contract, so pause,
//! upgrade, cap/fee changes and fund sweeps all require N-of-M approval
//! without any change to the role checks in those contracts.
//!
//! # Signatures
//! The signature argument is a `Vec<Signature>` sorted strictly ascending by
//! public key. Sorting makes duplicate signers impossible to smuggle in and
//! keeps verification O(n).
//!
//! # Self-administration
//! `add_signer`, `remove_signer` and `set_threshold` require the contract's
//! own authorization, i.e. `threshold` signatures over that call. The
//! threshold can never drop below [`MIN_THRESHOLD`] or exceed the signer count.

#![no_std]

use soroban_sdk::{
    auth::{Context, CustomAccountInterface},
    contract, contracterror, contractimpl, contracttype,
    crypto::Hash,
    symbol_short, BytesN, Env, Symbol, Vec,
};

/// A single-signer "multisig" is just a hot key with extra steps.
pub const MIN_THRESHOLD: u32 = 2;
/// Upper bound on signers, keeping `__check_auth` cost predictable.
pub const MAX_SIGNERS: u32 = 20;

const INSTANCE_BUMP_THRESHOLD: u32 = 17_280; // ~1 day of ledgers
const INSTANCE_BUMP_AMOUNT: u32 = 518_400; // ~30 days of ledgers

const MULTISIG: Symbol = symbol_short!("MULTISIG");

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum MultisigError {
    AlreadyInitialized = 1,
    NotInitialized = 2,
    InvalidThreshold = 3,
    InvalidSigners = 4,
    UnknownSigner = 5,
    SignaturesNotSorted = 6,
    NotEnoughSignatures = 7,
    SignerExists = 8,
}

/// One signer's approval of the authorization payload.
#[contracttype]
#[derive(Clone, Debug)]
pub struct Signature {
    pub public_key: BytesN<32>,
    pub signature: BytesN<64>,
}

#[contracttype]
#[derive(Clone)]
enum DataKey {
    Signers,
    Threshold,
}

#[contract]
pub struct MultisigAccount;

#[contractimpl]
impl MultisigAccount {
    /// Configure the signer set and threshold. Callable exactly once.
    ///
    /// Signers must be unique, 1..=[`MAX_SIGNERS`] of them, and
    /// `MIN_THRESHOLD <= threshold <= signers.len()`.
    pub fn initialize(
        env: Env,
        signers: Vec<BytesN<32>>,
        threshold: u32,
    ) -> Result<(), MultisigError> {
        if env.storage().instance().has(&DataKey::Threshold) {
            return Err(MultisigError::AlreadyInitialized);
        }
        if signers.is_empty() || signers.len() > MAX_SIGNERS || has_duplicates(&signers) {
            return Err(MultisigError::InvalidSigners);
        }
        validate_threshold(threshold, signers.len())?;

        env.storage().instance().set(&DataKey::Signers, &signers);
        env.storage()
            .instance()
            .set(&DataKey::Threshold, &threshold);
        bump(&env);

        env.events().publish(
            (MULTISIG, symbol_short!("INIT")),
            (signers.len(), threshold),
        );
        Ok(())
    }

    /// Add a signer. Requires `threshold` signatures from current signers.
    pub fn add_signer(env: Env, public_key: BytesN<32>) -> Result<(), MultisigError> {
        env.current_contract_address().require_auth();
        let mut signers = get_signers(&env)?;
        if signers.contains(&public_key) {
            return Err(MultisigError::SignerExists);
        }
        if signers.len() >= MAX_SIGNERS {
            return Err(MultisigError::InvalidSigners);
        }
        signers.push_back(public_key.clone());
        env.storage().instance().set(&DataKey::Signers, &signers);

        env.events()
            .publish((MULTISIG, symbol_short!("ADD_SIG")), public_key);
        Ok(())
    }

    /// Remove a signer. Requires `threshold` signatures. Refuses to leave
    /// fewer signers than the threshold, which would brick the account.
    pub fn remove_signer(env: Env, public_key: BytesN<32>) -> Result<(), MultisigError> {
        env.current_contract_address().require_auth();
        let mut signers = get_signers(&env)?;
        let idx = signers
            .first_index_of(&public_key)
            .ok_or(MultisigError::UnknownSigner)?;
        if signers.len() - 1 < get_threshold(&env)? {
            return Err(MultisigError::InvalidThreshold);
        }
        signers.remove(idx);
        env.storage().instance().set(&DataKey::Signers, &signers);

        env.events()
            .publish((MULTISIG, symbol_short!("DEL_SIG")), public_key);
        Ok(())
    }

    /// Change the threshold. Requires `threshold` signatures (the current one).
    pub fn set_threshold(env: Env, threshold: u32) -> Result<(), MultisigError> {
        env.current_contract_address().require_auth();
        let signers = get_signers(&env)?;
        validate_threshold(threshold, signers.len())?;
        env.storage()
            .instance()
            .set(&DataKey::Threshold, &threshold);

        env.events()
            .publish((MULTISIG, symbol_short!("SET_THR")), threshold);
        Ok(())
    }

    pub fn signers(env: Env) -> Result<Vec<BytesN<32>>, MultisigError> {
        get_signers(&env)
    }

    pub fn threshold(env: Env) -> Result<u32, MultisigError> {
        get_threshold(&env)
    }
}

#[contractimpl]
impl CustomAccountInterface for MultisigAccount {
    type Signature = Vec<Signature>;
    type Error = MultisigError;

    #[allow(non_snake_case)]
    fn __check_auth(
        env: Env,
        signature_payload: Hash<32>,
        signatures: Vec<Signature>,
        _auth_contexts: Vec<Context>,
    ) -> Result<(), MultisigError> {
        let signers = get_signers(&env)?;
        let threshold = get_threshold(&env)?;

        if signatures.len() < threshold {
            return Err(MultisigError::NotEnoughSignatures);
        }

        let payload = signature_payload.into();
        let mut previous: Option<BytesN<32>> = None;
        for sig in signatures.iter() {
            if let Some(prev) = &previous {
                if *prev >= sig.public_key {
                    return Err(MultisigError::SignaturesNotSorted);
                }
            }
            if !signers.contains(&sig.public_key) {
                return Err(MultisigError::UnknownSigner);
            }
            // Traps (fails the whole authorization) on an invalid signature.
            env.crypto()
                .ed25519_verify(&sig.public_key, &payload, &sig.signature);
            previous = Some(sig.public_key);
        }

        bump(&env);
        Ok(())
    }
}

fn get_signers(env: &Env) -> Result<Vec<BytesN<32>>, MultisigError> {
    env.storage()
        .instance()
        .get(&DataKey::Signers)
        .ok_or(MultisigError::NotInitialized)
}

fn get_threshold(env: &Env) -> Result<u32, MultisigError> {
    env.storage()
        .instance()
        .get(&DataKey::Threshold)
        .ok_or(MultisigError::NotInitialized)
}

fn validate_threshold(threshold: u32, signer_count: u32) -> Result<(), MultisigError> {
    if threshold < MIN_THRESHOLD || threshold > signer_count {
        return Err(MultisigError::InvalidThreshold);
    }
    Ok(())
}

fn has_duplicates(signers: &Vec<BytesN<32>>) -> bool {
    for i in 0..signers.len() {
        for j in (i + 1)..signers.len() {
            if signers.get_unchecked(i) == signers.get_unchecked(j) {
                return true;
            }
        }
    }
    false
}

fn bump(env: &Env) {
    env.storage()
        .instance()
        .extend_ttl(INSTANCE_BUMP_THRESHOLD, INSTANCE_BUMP_AMOUNT);
}

#[cfg(test)]
mod test;
