package stellar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrSequenceConflict  = errors.New("sequence number conflict")
	ErrSubmissionTimeout = errors.New("submission timeout")
	ErrDoubleSubmit      = errors.New("double submission prevented")
	// ErrSubmissionNotFound is returned by lookups when no chain_submissions
	// row matches — e.g. GetByIdempotencyKey on a key that was never used.
	ErrSubmissionNotFound = errors.New("submission not found")
)

type SubmissionStatus string

const (
	StatusPending   SubmissionStatus = "pending"
	StatusSubmitted SubmissionStatus = "submitted"
	StatusConfirmed SubmissionStatus = "confirmed"
	StatusFailed    SubmissionStatus = "failed"
	StatusUnknown   SubmissionStatus = "unknown"
)

type ChainSubmission struct {
	ID              uuid.UUID
	SourceAccount   string
	SequenceNumber  int64
	TransactionHash string
	SignedEnvelope  string
	Status          SubmissionStatus
	JobID           *uuid.UUID
	DomainAction    string
	IdempotencyKey  *string
	SubmittedAt     *time.Time
	ConfirmedAt     *time.Time
	ErrorMessage    *string
	RetryCount      int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type SubmissionPipeline struct {
	db           *sql.DB
	mu           sync.Mutex
	accountLocks map[string]*sync.Mutex

	// rpcURL/horizonURL/httpClient back the real mainnet lookups used by
	// seedSequenceFromNetwork and checkOnChainStatus. They're left zero by
	// default (see WithRPC) so existing callers/tests that only exercise
	// sequence allocation and record-keeping against Postgres are
	// unaffected.
	rpcURL     string
	horizonURL string
	httpClient *http.Client
}

func NewSubmissionPipeline(db *sql.DB) *SubmissionPipeline {
	return &SubmissionPipeline{
		db:           db,
		accountLocks: make(map[string]*sync.Mutex),
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// WithRPC enables real mainnet reconciliation: seedSequenceFromNetwork reads
// the account's current sequence from Horizon instead of assuming a fresh
// account starts at 0, and checkOnChainStatus polls Soroban RPC's
// getTransaction instead of always reporting unknown.
func (p *SubmissionPipeline) WithRPC(rpcURL, horizonURL string) *SubmissionPipeline {
	p.rpcURL = rpcURL
	p.horizonURL = horizonURL
	return p
}

func (p *SubmissionPipeline) getAccountLock(account string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()

	if lock, exists := p.accountLocks[account]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	p.accountLocks[account] = lock
	return lock
}

func (p *SubmissionPipeline) AllocateSequence(ctx context.Context, sourceAccount string) (int64, error) {
	accountLock := p.getAccountLock(sourceAccount)
	accountLock.Lock()
	defer accountLock.Unlock()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextSeq int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT next_sequence FROM account_sequences WHERE source_account = $1 FOR UPDATE`,
		sourceAccount,
	).Scan(&nextSeq)

	if err == sql.ErrNoRows {
		nextSeq, err = p.seedSequenceFromNetwork(ctx, sourceAccount)
		if err != nil {
			return 0, fmt.Errorf("seed sequence: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO account_sequences (source_account, next_sequence, last_synced_at, updated_at)
			 VALUES ($1, $2, NOW(), NOW())`,
			sourceAccount, nextSeq+1,
		)
		if err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE account_sequences SET next_sequence = $1, updated_at = NOW() WHERE source_account = $2`,
			nextSeq+1, sourceAccount,
		)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return nextSeq, nil
}

// seedSequenceFromNetwork reads sourceAccount's current sequence number from
// Horizon the first time we allocate for an account not yet tracked in
// account_sequences — e.g. after a fresh deploy, so our local counter starts
// in sync with the chain instead of silently colliding with whatever
// sequence the account is actually on. When no Horizon URL is configured
// (WithRPC was never called — the case in unit tests that only exercise
// Postgres bookkeeping) it falls back to 0, preserving prior behavior.
func (p *SubmissionPipeline) seedSequenceFromNetwork(ctx context.Context, sourceAccount string) (int64, error) {
	if p.horizonURL == "" {
		return 0, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.horizonURL+"/accounts/"+sourceAccount, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("horizon getAccount: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Sequence string `json:"sequence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode account response: %w", err)
	}
	seq, err := strconv.ParseInt(body.Sequence, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse sequence %q: %w", body.Sequence, err)
	}
	return seq, nil
}

func (p *SubmissionPipeline) RecordSubmission(
	ctx context.Context,
	sourceAccount string,
	sequenceNumber int64,
	signedEnvelope string,
	jobID *uuid.UUID,
	domainAction string,
) (*ChainSubmission, error) {
	txHash := p.computeTransactionHash(signedEnvelope)

	submission := &ChainSubmission{
		ID:              uuid.New(),
		SourceAccount:   sourceAccount,
		SequenceNumber:  sequenceNumber,
		TransactionHash: txHash,
		SignedEnvelope:  signedEnvelope,
		Status:          StatusPending,
		JobID:           jobID,
		DomainAction:    domainAction,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	query := `
		INSERT INTO chain_submissions 
		(id, source_account, sequence_number, transaction_hash, signed_envelope, status, job_id, domain_action, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err := p.db.ExecContext(
		ctx,
		query,
		submission.ID,
		submission.SourceAccount,
		submission.SequenceNumber,
		submission.TransactionHash,
		submission.SignedEnvelope,
		submission.Status,
		submission.JobID,
		submission.DomainAction,
		submission.CreatedAt,
		submission.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("record submission: %w", err)
	}

	return submission, nil
}

func (p *SubmissionPipeline) UpdateStatus(ctx context.Context, submissionID uuid.UUID, status SubmissionStatus, errMsg *string) error {
	query := `
		UPDATE chain_submissions 
		SET status = $1, error_message = $2, updated_at = NOW()
		WHERE id = $3
	`

	result, err := p.db.ExecContext(ctx, query, status, errMsg, submissionID)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		return errors.New("submission not found")
	}

	return nil
}

func (p *SubmissionPipeline) MarkSubmitted(ctx context.Context, submissionID uuid.UUID) error {
	query := `
		UPDATE chain_submissions 
		SET status = $1, submitted_at = NOW(), updated_at = NOW()
		WHERE id = $2
	`

	_, err := p.db.ExecContext(ctx, query, StatusSubmitted, submissionID)
	return err
}

func (p *SubmissionPipeline) MarkConfirmed(ctx context.Context, submissionID uuid.UUID) error {
	query := `
		UPDATE chain_submissions 
		SET status = $1, confirmed_at = NOW(), updated_at = NOW()
		WHERE id = $2
	`

	_, err := p.db.ExecContext(ctx, query, StatusConfirmed, submissionID)
	return err
}

func (p *SubmissionPipeline) ResolveTimeout(ctx context.Context, submissionID uuid.UUID) (SubmissionStatus, error) {
	submission, err := p.GetSubmission(ctx, submissionID)
	if err != nil {
		return StatusUnknown, err
	}

	onChainStatus := p.checkOnChainStatus(ctx, submission.TransactionHash)

	if onChainStatus == StatusConfirmed {
		if err := p.MarkConfirmed(ctx, submissionID); err != nil {
			return StatusUnknown, err
		}
		return StatusConfirmed, nil
	}

	if onChainStatus == StatusFailed {
		if err := p.UpdateStatus(ctx, submissionID, StatusFailed, nil); err != nil {
			return StatusUnknown, err
		}
		return StatusFailed, nil
	}

	return StatusUnknown, nil
}

// checkOnChainStatus asks Soroban RPC for txHash's current status. It never
// returns an error: a network hiccup or missing RPC configuration simply
// means the status stays unknown for now, which is the safe default for the
// ambiguous-timeout reconciliation callers (ResolveTimeout, SubmitIdempotent)
// — they treat unknown as "still can't tell, don't resubmit" rather than
// failing outright.
func (p *SubmissionPipeline) checkOnChainStatus(ctx context.Context, txHash string) SubmissionStatus {
	if p.rpcURL == "" || txHash == "" {
		return StatusUnknown
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getTransaction",
		"params":  map[string]string{"hash": txHash},
	})
	if err != nil {
		return StatusUnknown
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.rpcURL, bytes.NewReader(body))
	if err != nil {
		return StatusUnknown
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return StatusUnknown
	}
	defer resp.Body.Close()

	var decoded struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil || decoded.Error != nil {
		return StatusUnknown
	}

	switch decoded.Result.Status {
	case "SUCCESS":
		return StatusConfirmed
	case "FAILED":
		return StatusFailed
	default:
		// "NOT_FOUND" (still pending, or evicted after the RPC node's
		// retention window) — unknown, not failed, so a timed-out
		// submission is never mistaken for a definitive rejection.
		return StatusUnknown
	}
}

func (p *SubmissionPipeline) GetSubmission(ctx context.Context, submissionID uuid.UUID) (*ChainSubmission, error) {
	query := `
		SELECT id, source_account, sequence_number, transaction_hash, signed_envelope,
		       status, job_id, domain_action, idempotency_key, submitted_at, confirmed_at,
		       error_message, retry_count, created_at, updated_at
		FROM chain_submissions
		WHERE id = $1
	`

	var submission ChainSubmission
	err := p.db.QueryRowContext(ctx, query, submissionID).Scan(
		&submission.ID,
		&submission.SourceAccount,
		&submission.SequenceNumber,
		&submission.TransactionHash,
		&submission.SignedEnvelope,
		&submission.Status,
		&submission.JobID,
		&submission.DomainAction,
		&submission.IdempotencyKey,
		&submission.SubmittedAt,
		&submission.ConfirmedAt,
		&submission.ErrorMessage,
		&submission.RetryCount,
		&submission.CreatedAt,
		&submission.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrSubmissionNotFound
	}
	if err != nil {
		return nil, err
	}

	return &submission, nil
}

// GetByIdempotencyKey returns the most recent submission recorded for
// (sourceAccount, idempotencyKey), or ErrSubmissionNotFound if that key has
// never been submitted. This is the lookup SubmitIdempotent uses to detect a
// retried call before deciding whether to send a transaction.
func (p *SubmissionPipeline) GetByIdempotencyKey(ctx context.Context, sourceAccount, idempotencyKey string) (*ChainSubmission, error) {
	query := `
		SELECT id, source_account, sequence_number, transaction_hash, signed_envelope,
		       status, job_id, domain_action, idempotency_key, submitted_at, confirmed_at,
		       error_message, retry_count, created_at, updated_at
		FROM chain_submissions
		WHERE source_account = $1 AND idempotency_key = $2
		ORDER BY created_at DESC
		LIMIT 1
	`

	var submission ChainSubmission
	err := p.db.QueryRowContext(ctx, query, sourceAccount, idempotencyKey).Scan(
		&submission.ID,
		&submission.SourceAccount,
		&submission.SequenceNumber,
		&submission.TransactionHash,
		&submission.SignedEnvelope,
		&submission.Status,
		&submission.JobID,
		&submission.DomainAction,
		&submission.IdempotencyKey,
		&submission.SubmittedAt,
		&submission.ConfirmedAt,
		&submission.ErrorMessage,
		&submission.RetryCount,
		&submission.CreatedAt,
		&submission.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrSubmissionNotFound
	}
	if err != nil {
		return nil, err
	}

	return &submission, nil
}

// RecordSubmissionWithKey is RecordSubmission extended with the two fields
// the idempotent submission path (SubmitIdempotent) needs: a caller-supplied
// idempotency key to dedupe retries against, and the transaction hash
// computed locally from the signed envelope before it's ever sent — so the
// hash is available for chain-status reconciliation even if the RPC call
// that follows times out.
func (p *SubmissionPipeline) RecordSubmissionWithKey(
	ctx context.Context,
	sourceAccount string,
	sequenceNumber int64,
	signedEnvelope, txHash, idempotencyKey string,
	jobID *uuid.UUID,
	domainAction string,
) (*ChainSubmission, error) {
	submission := &ChainSubmission{
		ID:              uuid.New(),
		SourceAccount:   sourceAccount,
		SequenceNumber:  sequenceNumber,
		TransactionHash: txHash,
		SignedEnvelope:  signedEnvelope,
		Status:          StatusPending,
		JobID:           jobID,
		DomainAction:    domainAction,
		IdempotencyKey:  &idempotencyKey,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	query := `
		INSERT INTO chain_submissions
		(id, source_account, sequence_number, transaction_hash, signed_envelope, status, job_id, domain_action, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`

	_, err := p.db.ExecContext(
		ctx,
		query,
		submission.ID,
		submission.SourceAccount,
		submission.SequenceNumber,
		submission.TransactionHash,
		submission.SignedEnvelope,
		submission.Status,
		submission.JobID,
		submission.DomainAction,
		submission.IdempotencyKey,
		submission.CreatedAt,
		submission.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	return submission, nil
}

// RecordOrGetExisting inserts a new submission for idempotencyKey, or — if a
// concurrent caller already claimed that key first (the unique index added
// in migration 102) — fetches and returns that existing row instead of
// erroring. isNew tells SubmitIdempotent which happened: true means this
// call owns the submission and must go on to send it; false means another
// attempt is already in flight and this call must not send anything.
func (p *SubmissionPipeline) RecordOrGetExisting(
	ctx context.Context,
	sourceAccount, idempotencyKey string,
	sequenceNumber int64,
	signedEnvelope, txHash string,
	jobID *uuid.UUID,
	domainAction string,
) (submission *ChainSubmission, isNew bool, err error) {
	submission, err = p.RecordSubmissionWithKey(ctx, sourceAccount, sequenceNumber, signedEnvelope, txHash, idempotencyKey, jobID, domainAction)
	if err == nil {
		return submission, true, nil
	}

	if !isUniqueViolation(err) {
		return nil, false, err
	}

	existing, getErr := p.GetByIdempotencyKey(ctx, sourceAccount, idempotencyKey)
	if getErr != nil {
		return nil, false, fmt.Errorf("fetch existing submission after conflict: %w", getErr)
	}
	return existing, false, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — specifically the one RecordOrGetExisting
// expects from the idx_chain_submissions_idempotency_key index when a
// concurrent caller wins the race for the same idempotency key.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func (p *SubmissionPipeline) GetPendingSubmissions(ctx context.Context, sourceAccount string) ([]ChainSubmission, error) {
	query := `
		SELECT id, source_account, sequence_number, transaction_hash, signed_envelope,
		       status, job_id, domain_action, idempotency_key, submitted_at, confirmed_at,
		       error_message, retry_count, created_at, updated_at
		FROM chain_submissions
		WHERE source_account = $1 AND status IN ('pending', 'submitted', 'unknown')
		ORDER BY sequence_number ASC
	`

	rows, err := p.db.QueryContext(ctx, query, sourceAccount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var submissions []ChainSubmission
	for rows.Next() {
		var submission ChainSubmission
		err := rows.Scan(
			&submission.ID,
			&submission.SourceAccount,
			&submission.SequenceNumber,
			&submission.TransactionHash,
			&submission.SignedEnvelope,
			&submission.Status,
			&submission.JobID,
			&submission.DomainAction,
			&submission.IdempotencyKey,
			&submission.SubmittedAt,
			&submission.ConfirmedAt,
			&submission.ErrorMessage,
			&submission.RetryCount,
			&submission.CreatedAt,
			&submission.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		submissions = append(submissions, submission)
	}

	return submissions, rows.Err()
}

func (p *SubmissionPipeline) RecoverOnStartup(ctx context.Context) error {
	query := `
		SELECT DISTINCT source_account 
		FROM chain_submissions 
		WHERE status IN ('pending', 'submitted', 'unknown')
	`

	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	var accounts []string
	for rows.Next() {
		var account string
		if err := rows.Scan(&account); err != nil {
			return err
		}
		accounts = append(accounts, account)
	}

	for _, account := range accounts {
		pending, err := p.GetPendingSubmissions(ctx, account)
		if err != nil {
			return fmt.Errorf("get pending for %s: %w", account, err)
		}

		for _, submission := range pending {
			status, err := p.ResolveTimeout(ctx, submission.ID)
			if err != nil {
				return fmt.Errorf("resolve submission %s: %w", submission.ID, err)
			}
			_ = status
		}
	}

	return nil
}

func (p *SubmissionPipeline) computeTransactionHash(signedEnvelope string) string {
	envelopeBytes := []byte(signedEnvelope)
	hash := sha256.Sum256(envelopeBytes)
	return hex.EncodeToString(hash[:])
}

func (p *SubmissionPipeline) DetectSequenceGap(ctx context.Context, sourceAccount string) ([]int64, error) {
	query := `
		SELECT sequence_number 
		FROM chain_submissions 
		WHERE source_account = $1 AND status IN ('confirmed', 'submitted')
		ORDER BY sequence_number ASC
	`

	rows, err := p.db.QueryContext(ctx, query, sourceAccount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sequences []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			return nil, err
		}
		sequences = append(sequences, seq)
	}

	if len(sequences) < 2 {
		return nil, nil
	}

	var gaps []int64
	for i := 1; i < len(sequences); i++ {
		expected := sequences[i-1] + 1
		if sequences[i] != expected {
			for gap := expected; gap < sequences[i]; gap++ {
				gaps = append(gaps, gap)
			}
		}
	}

	return gaps, nil
}
