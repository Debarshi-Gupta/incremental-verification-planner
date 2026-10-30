package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrIdempotencyConflict = errors.New("idempotency key was already used with a different request")

type IdempotentOperation func() (status int, response []byte, err error)

// RunIdempotent serializes requests with the same tenant/route/key, rejects a
// changed request hash, and stores the exact JSON response for safe replay.
func (d *DB) RunIdempotent(ctx context.Context, tenantID uuid.UUID, endpoint, key, requestHash string, operation IdempotentOperation) (int, []byte, bool, error) {
	if key == "" || len(key) > 200 {
		return 0, nil, false, errors.New("valid Idempotency-Key header required")
	}
	if endpoint == "" || requestHash == "" || operation == nil {
		return 0, nil, false, errors.New("idempotency endpoint, request hash, and operation required")
	}
	status := 0
	var response []byte
	replayed := false
	err := d.TenantTx(ctx, tenantID.String(), func(tx pgx.Tx) error {
		lockName := tenantID.String() + "/" + endpoint + "/" + key
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockName); err != nil {
			return err
		}
		var storedHash string
		var storedStatus *int
		var storedResponse []byte
		err := tx.QueryRow(ctx, `SELECT request_hash,response_status,response_json FROM idempotency_requests WHERE tenant_id=$1 AND endpoint=$2 AND idempotency_key=$3`, tenantID, endpoint, key).Scan(&storedHash, &storedStatus, &storedResponse)
		if err == nil {
			if storedHash != requestHash {
				return ErrIdempotencyConflict
			}
			if storedStatus == nil || len(storedResponse) == 0 {
				return errors.New("idempotency record is incomplete")
			}
			status = *storedStatus
			response = storedResponse
			replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var opErr error
		status, response, opErr = operation()
		if opErr != nil {
			return opErr
		}
		if status < 100 || status > 599 {
			return fmt.Errorf("operation returned invalid HTTP status %d", status)
		}
		if !jsonValid(response) {
			return errors.New("idempotent operation response must be valid JSON")
		}
		_, err = tx.Exec(ctx, `INSERT INTO idempotency_requests(tenant_id,endpoint,idempotency_key,request_hash,response_status,response_json) VALUES($1,$2,$3,$4,$5,$6)`, tenantID, endpoint, key, requestHash, status, response)
		return err
	})
	return status, response, replayed, err
}

func HashRequest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func jsonValid(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	return json.Unmarshal(raw, &value) == nil
}
