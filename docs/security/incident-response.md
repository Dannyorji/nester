# Incident Response Runbook: Mainnet Exploit or Fund Loss

Owner: security lead · Tracked in #1375 · Review: after every incident and
every tabletop exercise.

> Anything in `<angle brackets>` still has to be filled in before launch. A
> runbook with placeholders counts as unfinished.

**If you think funds are at risk right now:** page the on-call (§2), then go
straight to [§3 Emergency halt](#3-emergency-halt). Pausing early and being
wrong costs a little. Waiting too long can cost user funds.

---

## 1. Severity

| Sev | Definition | Examples | Halt? | Response time |
|---|---|---|---|---|
| **Sev-1** | Funds lost or being actively drained, or an exploit path confirmed | Unexpected outflows, share-price manipulation, compromised Admin quorum | **Yes, immediately** | Page now, respond in 15 min, 24/7 |
| **Sev-2** | Credible threat to funds that isn't being exploited yet | Private vuln report with a PoC, one multisig signer key leaked, unexpected pending upgrade, oracle/adapter misbehaving | Usually. The Incident Commander decides. | Page now, respond in 1h |
| **Sev-3** | Degraded but safe | Circuit breaker tripped on its own, offramp provider outage, RPC outage | No | Next business day |

The circuit breaker ([SECURITY.md](../../SECURITY.md#circuit-breaker-issue-817))
may already have tripped by the time a human looks. That's a signal, not a
resolution.

## 2. Who gets paged

### Roles

| Role | Responsibility | Primary | Backup |
|---|---|---|---|
| **Incident Commander (IC)** | Owns decisions: halt, comms, when to close. Doesn't debug. | `<name>` | `<name>` |
| **Guardian signers** | Sign emergency halts (Guardian multisig, 2-of-7) | `<7 names>` | n/a |
| **Admin signers** | Sign cancellations, recovery and upgrades (Admin multisig, 3-of-5) | `<5 names>` | n/a |
| **Investigator** | Traces the on-chain activity and finds the root cause | `<name>` | `<name>` |
| **Comms lead** | Status page, X/Twitter, Discord/Telegram, partner and exchange contacts | `<name>` | `<name>` |
| **Scribe** | Keeps a timestamped log in the incident channel | whoever joins third | n/a |

### Paging

- **Paging tool:** `<PagerDuty / Opsgenie service: nester-mainnet-sev1>`, with 5-minute escalation to the backup and then to all Guardian signers.
- **War room:** `<private channel #inc-YYYYMMDD-short-name>`, with a bridge link pinned. Don't discuss the incident in public channels.
- **Out-of-band fallback** (if chat or email may be compromised): `<Signal group "Nester Guardians">`.
- **Automatic triggers** that page Sev-1:
  - the circuit breaker escalating to `DepositsHalted` or `FullHalt`
  - withdrawal velocity above threshold
  - a `PROP_UPG` event (proposed upgrade) or a `CTR_PROP` event (proposed orchestrator address change) that isn't on the change calendar
  - any role GRANT or REVOKE event on a protocol contract
  - `verify-mainnet-roles.sh` failing on its schedule
  - treasury outflow above `<threshold>`

  Wiring these alerts to the indexer is a launch prerequisite: `<link to alert config>`.

### External contacts (keep these current)

| Who | Why | Contact |
|---|---|---|
| Stellar Development Foundation security | Network-level coordination, known-attacker info | `<contact>` |
| Circle | USDC freeze requests for stolen funds | `<contact>` |
| Integrated lending/yield protocols (Blend, …) | Adapter-level exposure | `<contacts>` |
| Paystack / Flutterwave | Suspend offramp payouts | `<contacts>` |
| Auditor(s) | Emergency review of fixes | `<contacts>` |
| Legal counsel | Disclosure obligations, law enforcement | `<contact>` |
| Security Alliance (SEAL 911) or similar | Incident help, tracing | `<contact>` |

## 3. Emergency halt

The goal is to stop the bleeding without trapping users. **The emergency
withdrawal queue stays open at every breaker severity, including
`FullHalt`.** That's deliberate: never try to block user exits.

### 3.1 On-chain levers (Guardian multisig, 2 signatures)

A Guardian can only make vaults safer, so any two Guardian signers can act
without further approval. Pick the smallest lever that stops the attack:

| Lever | Call (per vault) | Effect |
|---|---|---|
| Halt new deposits | `guardian_halt_deposits(caller = GUARDIAN_MULTISIG)` | Stops inflows; withdrawals continue |
| Full halt | `guardian_trip_breaker(caller = GUARDIAN_MULTISIG)` | Stops everything except the emergency withdrawal queue |
| Pause | `pause(caller = GUARDIAN_MULTISIG)` | Stops all vault operations |

Apply it to **every** vault (USDC, XLM, and any from the factory). Attackers
move to whatever is still open.

Signing is described in [admin-multisig.md](admin-multisig.md#signing-a-multisig-transaction).
Keep pre-built, unsigned halt transactions for each vault in `<location>`,
refreshed weekly, so an emergency halt only needs two signatures, not
building from scratch under pressure.

> The admin API's pause endpoint returns `409 MULTISIG_REQUIRED` on mainnet
> by design (#1374). Don't waste time on it.

### 3.2 Admin multisig actions (3 signatures)

- **Cancel a suspicious pending upgrade:** `cancel_upgrade` on the affected
  contract. Check `get_pending_upgrade()` on the vaults, treasury, registry,
  strategy, orchestrator and recurring_deposit.
- **Cancel a suspicious orchestrator address change:**
  `cancel_update_contract(kind)`. Check `get_pending_update(kind)` for every
  kind.
- **Revoke a compromised role holder:** `revoke_role`. If a multisig
  *signer* is compromised, rotate them out (see
  [admin-multisig.md](admin-multisig.md#rotating-a-signer)).

### 3.3 Off-chain levers (API operator; needs a deploy or restart)

| Lever | How |
|---|---|
| Stop the automated rebalancer | `REBALANCER_ENABLED=false` |
| Stop recurring deposit pulls | `RECURRING_DEPOSIT_ENABLED=false` |
| Stop the harvest engine | `HARVEST_ENGINE_ENABLED=false` |
| Stop fiat payouts | Suspend payouts in the Paystack/Flutterwave dashboards |
| Frontend banner or deposit block | `<frontend kill switch / maintenance mode>` |

### 3.4 Evidence (start as soon as the halt is in)

- Record the tx hashes, ledger sequence numbers, attacker addresses and
  timestamps in the incident log.
- Snapshot the contract state (`get_breaker_status`, balances, share price,
  pending upgrades) and API/indexer logs **before** any recovery action.
- Don't wipe or redeploy anything the investigator hasn't cleared.

## 4. Communicating with users

Principles:
- Say what users should do **first**.
- Never ask users to sign anything or connect a wallet on a new site during
  an incident. Attackers phish during incidents.
- Only state what you know. Say when the next update will come, and keep
  that promise.
- Post the same text on every channel. The status page is canonical.

### 4.1 Initial notice (within 30 minutes of a Sev-1 halt)

> **We're investigating an incident affecting Nester vaults.**
> As a precaution we've paused `<deposits / all vault operations>`
> on `<vaults>`. Withdrawals through the emergency withdrawal queue are
> `<available / temporarily unavailable>`.
>
> **What you should do:** Nothing is needed from you right now. Don't
> interact with links or "recovery" sites claiming to be Nester. We will
> never DM you or ask you to sign a transaction to fix this.
>
> Next update by `<time UTC>` at `<status page URL>`.

### 4.2 Update (at least every 2 hours while Sev-1 is open)

> **Update `<n>` on the `<date>` incident.** `<What we now know, in one or
> two sentences.>` `<What is still paused.>` `<What users can do, e.g. the
> emergency withdrawal queue is open.>` Next update by `<time UTC>`.

### 4.3 Resolved

> **The `<date>` incident is resolved.** `<Vaults/operations>` are running
> normally again as of `<time UTC>`. `<Were user funds affected? If yes: how
> much, who, and the remediation plan.>` A full post-mortem will be published
> by `<date, within 14 days>`.

### 4.4 Private notices

Send these to integrated protocols, exchanges and offramp partners. Tell
them what's paused, which addresses to watch (attacker addresses), and give
a contact for coordination.

## 5. Recovery

1. The root cause is understood, and a fix is written and **independently
   reviewed** (the auditor, if possible).
2. The fix ships through the normal timelocked upgrade: `propose_upgrade`,
   then the full 48h (vault) or 7-day (treasury) window. **Don't shorten the
   timelock during an incident.** It protects users from the recovery going
   wrong too.
3. Staged unpause: `recover_next_stage` (Admin/Upgrader) steps the breaker
   down one stage at a time after each cooldown, then `unpause`. Watch each
   stage before taking the next.
4. Re-run `verify-mainnet-roles.sh` and confirm no unexpected role changes.
5. Turn the off-chain jobs back on one at a time.

## 6. Post-incident disclosure

| When | What |
|---|---|
| Resolution + 72h | Internal post-mortem draft (blameless): timeline, root cause, impact, what worked, what didn't, action items with owners |
| Resolution + 14 days | **Public post-mortem** at `<blog/status URL>`, linked from every channel that got the initial notice |
| With a reporter | Coordinated disclosure under [SECURITY.md](../../SECURITY.md#response-timeline), with credit and bounty per policy |
| As required | Regulatory, law-enforcement and partner notifications, as legal counsel advises |

The public post-mortem covers:
- a timeline in UTC;
- the root cause, technical enough for other teams to learn from, but only
  after every affected deployment (including forks and integrations we know
  about) is patched;
- the impact, including funds affected and users affected;
- the remediation and reimbursement plan, if any;
- the action items and when they'll be done.

Don't publish exploit details while an unpatched deployment is still exposed.

## 7. Tabletop exercise

**This has to happen before mainnet launch**, then quarterly and whenever the
on-call roster or multisig signers change. Allow about 90 minutes: one
facilitator, everyone in §2, with production access simulated (use testnet
multisigs for any real signing).

### Scenario A: active drain (Sev-1)
*T+0:* An alert fires for withdrawal velocity above threshold on the USDC
vault. *T+5:* Share price has dropped 12% in 10 minutes. *T+12:* A Twitter
account with 50k followers posts "Nester is being drained?"

Check that the team:
- paged and assembled within 15 minutes;
- got two Guardian signatures on `guardian_trip_breaker` for **every** vault
  (actually sign on testnet, and time it);
- posted the initial notice within 30 minutes;
- confirmed the emergency withdrawal queue still works;
- started collecting evidence before taking any recovery action.

### Scenario B: hostile upgrade (Sev-2)
A `PROP_UPG` event appears on the treasury with a WASM hash nobody
recognises, ETA 7 days away. Two days later, an Admin signer reports their
laptop was stolen.

Check that the team:
- cancelled the upgrade with the Admin multisig;
- rotated the lost signer out (`add_signer` then `remove_signer`) and
  re-verified roles;
- decided on user comms: publish or not, and why.

### Scenario C: config mistake (Sev-2)
A staging deploy accidentally gets mainnet contract addresses.

Check that:
- the API refused to boot (#1371). Confirm by actually running it with the
  bad config.
- the team found and fixed the root cause in the deploy pipeline.

### Exercise record

| Date | Facilitator | Scenarios | Time to halt (all vaults) | Time to first notice | Gaps found → issue |
|---|---|---|---|---|---|
| `<before launch>` | | | | | |

Launch sign-off requires at least one completed row with every gap either
fixed or explicitly accepted by the security lead.
