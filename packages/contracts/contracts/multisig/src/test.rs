#![cfg(test)]

extern crate std;

use ed25519_dalek::{Signer as _, SigningKey};
use soroban_sdk::{
    auth::Context,
    testutils::{Address as _, BytesN as _},
    vec,
    xdr::{
        self, HashIdPreimage, HashIdPreimageSorobanAuthorization, InvokeContractArgs, Limits,
        ScAddress, ScSymbol, ScVal, SorobanAddressCredentials, SorobanAuthorizationEntry,
        SorobanAuthorizedFunction, SorobanAuthorizedInvocation, SorobanCredentials, VecM, WriteXdr,
    },
    Address, BytesN, Env, IntoVal, TryFromVal, Val, Vec,
};

use crate::{MultisigAccount, MultisigAccountClient, MultisigError, Signature};

fn keypair(seed: u8) -> SigningKey {
    SigningKey::from_bytes(&[seed; 32])
}

fn public_key(env: &Env, key: &SigningKey) -> BytesN<32> {
    BytesN::from_array(env, &key.verifying_key().to_bytes())
}

/// Sign `payload` with each key, sorted ascending by public key as the
/// contract requires.
fn sign(env: &Env, payload: &BytesN<32>, keys: &[&SigningKey]) -> Vec<Signature> {
    let mut sorted: std::vec::Vec<&SigningKey> = keys.to_vec();
    sorted.sort_by_key(|k| k.verifying_key().to_bytes());
    let mut out = Vec::new(env);
    for k in sorted {
        out.push_back(Signature {
            public_key: public_key(env, k),
            signature: BytesN::from_array(env, &k.sign(&payload.to_array()).to_bytes()),
        });
    }
    out
}

/// 2-of-3 multisig over keys seeded 1, 2, 3.
fn setup(env: &Env) -> (Address, MultisigAccountClient<'_>, [SigningKey; 3]) {
    let keys = [keypair(1), keypair(2), keypair(3)];
    let id = env.register_contract(None, MultisigAccount);
    let client = MultisigAccountClient::new(env, &id);
    let signers = vec![
        env,
        public_key(env, &keys[0]),
        public_key(env, &keys[1]),
        public_key(env, &keys[2]),
    ];
    client.initialize(&signers, &2);
    (id, client, keys)
}

fn check_auth(
    env: &Env,
    id: &Address,
    payload: &BytesN<32>,
    sigs: Vec<Signature>,
) -> Result<(), MultisigError> {
    let contexts: Vec<Context> = Vec::new(env);
    env.try_invoke_contract_check_auth::<MultisigError>(id, payload, sigs.into_val(env), &contexts)
        .map_err(|e| match e {
            Ok(err) => err,
            Err(_) => MultisigError::InvalidSigners, // host trap, e.g. bad signature
        })
}

// ── Initialization ────────────────────────────────────────────────────────────

#[test]
fn initialize_stores_signers_and_threshold() {
    let env = Env::default();
    let (_, client, _) = setup(&env);
    assert_eq!(client.threshold(), 2);
    assert_eq!(client.signers().len(), 3);
}

#[test]
fn initialize_rejects_bad_config() {
    let env = Env::default();
    let a = BytesN::random(&env);
    let b = BytesN::random(&env);

    let cases: [(Vec<BytesN<32>>, u32, MultisigError); 4] = [
        (
            vec![&env, a.clone(), b.clone()],
            1,
            MultisigError::InvalidThreshold,
        ),
        (
            vec![&env, a.clone(), b.clone()],
            3,
            MultisigError::InvalidThreshold,
        ),
        (
            vec![&env, a.clone(), a.clone()],
            2,
            MultisigError::InvalidSigners,
        ),
        (Vec::new(&env), 2, MultisigError::InvalidSigners),
    ];
    for (signers, threshold, want) in cases {
        let id = env.register_contract(None, MultisigAccount);
        let client = MultisigAccountClient::new(&env, &id);
        assert_eq!(client.try_initialize(&signers, &threshold), Err(Ok(want)));
    }
}

#[test]
fn initialize_twice_fails() {
    let env = Env::default();
    let (_, client, _) = setup(&env);
    let signers = client.signers();
    assert_eq!(
        client.try_initialize(&signers, &2),
        Err(Ok(MultisigError::AlreadyInitialized))
    );
}

// ── __check_auth ──────────────────────────────────────────────────────────────

#[test]
fn threshold_signatures_authorize() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let payload = BytesN::random(&env);

    assert_eq!(
        check_auth(&env, &id, &payload, sign(&env, &payload, &[&k[0], &k[2]])),
        Ok(())
    );
    assert_eq!(
        check_auth(
            &env,
            &id,
            &payload,
            sign(&env, &payload, &[&k[0], &k[1], &k[2]])
        ),
        Ok(())
    );
}

#[test]
fn single_signature_is_rejected() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let payload = BytesN::random(&env);

    assert_eq!(
        check_auth(&env, &id, &payload, sign(&env, &payload, &[&k[1]])),
        Err(MultisigError::NotEnoughSignatures)
    );
}

#[test]
fn unknown_signer_is_rejected() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let outsider = keypair(9);
    let payload = BytesN::random(&env);

    assert_eq!(
        check_auth(
            &env,
            &id,
            &payload,
            sign(&env, &payload, &[&k[0], &outsider])
        ),
        Err(MultisigError::UnknownSigner)
    );
}

#[test]
fn duplicate_signer_cannot_meet_threshold() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let payload = BytesN::random(&env);

    let one = sign(&env, &payload, &[&k[0]]).get_unchecked(0);
    let doubled = vec![&env, one.clone(), one];
    assert_eq!(
        check_auth(&env, &id, &payload, doubled),
        Err(MultisigError::SignaturesNotSorted)
    );
}

#[test]
fn unsorted_signatures_are_rejected() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let payload = BytesN::random(&env);

    let sorted = sign(&env, &payload, &[&k[0], &k[1]]);
    let reversed = vec![&env, sorted.get_unchecked(1), sorted.get_unchecked(0)];
    assert_eq!(
        check_auth(&env, &id, &payload, reversed),
        Err(MultisigError::SignaturesNotSorted)
    );
}

#[test]
fn signature_over_a_different_payload_is_rejected() {
    let env = Env::default();
    let (id, _, k) = setup(&env);
    let payload = BytesN::random(&env);
    let other = BytesN::random(&env);

    // Valid signatures, but over another payload: ed25519_verify traps.
    assert!(check_auth(&env, &id, &payload, sign(&env, &other, &[&k[0], &k[1]])).is_err());
}

// ── Self-administration ──────────────────────────────────────────────────────

#[test]
fn signer_management_enforces_threshold_bounds() {
    let env = Env::default();
    env.mock_all_auths();
    let (_, client, k) = setup(&env);

    // Cannot drop below 2-of-N or above N-of-N.
    assert_eq!(
        client.try_set_threshold(&1),
        Err(Ok(MultisigError::InvalidThreshold))
    );
    assert_eq!(
        client.try_set_threshold(&4),
        Err(Ok(MultisigError::InvalidThreshold))
    );
    client.set_threshold(&3);

    // With 3-of-3, removing a signer would brick the account.
    assert_eq!(
        client.try_remove_signer(&public_key(&env, &k[0])),
        Err(Ok(MultisigError::InvalidThreshold))
    );

    let new_key = public_key(&env, &keypair(4));
    client.add_signer(&new_key);
    assert_eq!(
        client.try_add_signer(&new_key),
        Err(Ok(MultisigError::SignerExists))
    );
    client.remove_signer(&public_key(&env, &k[0]));
    assert_eq!(client.signers().len(), 3);
}

#[test]
fn signer_management_requires_the_account_itself() {
    let env = Env::default();
    let (_, client, _) = setup(&env);
    // No auth mocked: the account's own threshold approval is missing.
    assert!(client.try_set_threshold(&3).is_err());
    assert!(client.try_add_signer(&BytesN::random(&env)).is_err());
    assert_eq!(client.threshold(), 2);
}

// ── End to end: multisig holding Admin on a protocol contract ───────────────

/// Build a signed authorization entry for `multisig` approving
/// `contract.function(args)`, exactly as a wallet would submit it.
fn signed_entry(
    env: &Env,
    multisig: &Address,
    contract: &Address,
    function: &str,
    args: std::vec::Vec<Val>,
    keys: &[&SigningKey],
) -> SorobanAuthorizationEntry {
    let nonce = 7;
    let expiration = env.ledger().sequence() + 100;
    let sc_args: std::vec::Vec<ScVal> = args
        .into_iter()
        .map(|v| ScVal::try_from_val(env, &v).unwrap())
        .collect();
    let invocation = SorobanAuthorizedInvocation {
        function: SorobanAuthorizedFunction::ContractFn(InvokeContractArgs {
            contract_address: ScAddress::from(contract),
            function_name: ScSymbol(function.try_into().unwrap()),
            args: VecM::try_from(sc_args).unwrap(),
        }),
        sub_invocations: VecM::default(),
    };

    let preimage = HashIdPreimage::SorobanAuthorization(HashIdPreimageSorobanAuthorization {
        network_id: xdr::Hash(env.ledger().network_id().to_array()),
        nonce,
        signature_expiration_ledger: expiration,
        invocation: invocation.clone(),
    });
    let bytes = preimage.to_xdr(Limits::none()).unwrap();
    let payload: BytesN<32> = env
        .crypto()
        .sha256(&soroban_sdk::Bytes::from_slice(env, &bytes))
        .into();

    let signatures: Val = sign(env, &payload, keys).into_val(env);
    SorobanAuthorizationEntry {
        credentials: SorobanCredentials::Address(SorobanAddressCredentials {
            address: ScAddress::from(multisig),
            nonce,
            signature_expiration_ledger: expiration,
            signature: ScVal::try_from_val(env, &signatures).unwrap(),
        }),
        root_invocation: invocation,
    }
}

#[test]
fn protocol_admin_action_needs_threshold_signatures() {
    use nester_access_control::Role;
    use nester_contract::{ContractKind, NesterContract, NesterContractClient, ProtocolInitConfig};

    let env = Env::default();
    let (multisig, _, k) = setup(&env);

    // Deploy the orchestrator and hand Admin to the multisig.
    let deployer = Address::generate(&env);
    let nester_id = env.register_contract(None, NesterContract);
    let nester = NesterContractClient::new(&env, &nester_id);
    env.mock_all_auths();
    nester.initialize(
        &deployer,
        &ProtocolInitConfig {
            vault_usdc: Address::generate(&env),
            vault_xlm: Address::generate(&env),
            vault_token_usdc: Address::generate(&env),
            vault_token_xlm: Address::generate(&env),
            treasury: Address::generate(&env),
            yield_registry: Address::generate(&env),
            allocation_strategy: Address::generate(&env),
        },
    );
    nester.grant_role(&deployer, &multisig, &Role::Admin);
    nester.revoke_role(&multisig, &deployer, &Role::Admin);
    assert!(!nester.has_role(&deployer, &Role::Admin));

    // From here on, auth is real: only signed entries are accepted.
    let new_treasury = Address::generate(&env);
    let args = std::vec![
        multisig.into_val(&env),
        ContractKind::Treasury.into_val(&env),
        new_treasury.into_val(&env),
    ];

    // One signer (a single compromised key) cannot act as Admin.
    env.set_auths(&[signed_entry(
        &env,
        &multisig,
        &nester_id,
        "propose_update_contract",
        args.clone(),
        &[&k[0]],
    )]);
    assert!(nester
        .try_propose_update_contract(&multisig, &ContractKind::Treasury, &new_treasury)
        .is_err());
    assert!(nester.get_pending_update(&ContractKind::Treasury).is_none());

    // Two of three signers can.
    env.set_auths(&[signed_entry(
        &env,
        &multisig,
        &nester_id,
        "propose_update_contract",
        args,
        &[&k[0], &k[2]],
    )]);
    nester.propose_update_contract(&multisig, &ContractKind::Treasury, &new_treasury);
    assert_eq!(
        nester
            .get_pending_update(&ContractKind::Treasury)
            .unwrap()
            .new_address,
        new_treasury
    );
}
