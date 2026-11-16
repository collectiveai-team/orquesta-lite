package workflow

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/collectiveai-team/orquesta-lite/internal/external"
)

const schemaV5 = `CREATE TABLE external_usage (
 attempt_id TEXT NOT NULL REFERENCES external_attempts(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL,
 cost_usd REAL,
 input_tokens INTEGER,
 output_tokens INTEGER,
 PRIMARY KEY(attempt_id,run_id)
);
INSERT INTO external_usage(attempt_id,run_id,cost_usd,input_tokens,output_tokens)
SELECT id,COALESCE(NULLIF(json_extract(result_json,'$.references.runId'),''),json_extract(references_json,'$.runId'),''),
 json_extract(result_json,'$.costUSD'),json_extract(result_json,'$.inputTokens'),json_extract(result_json,'$.outputTokens')
FROM external_attempts WHERE json_valid(result_json) AND
 (json_extract(result_json,'$.costUSD') IS NOT NULL OR json_extract(result_json,'$.inputTokens') IS NOT NULL OR json_extract(result_json,'$.outputTokens') IS NOT NULL);`

func recordExternalUsage(ctx context.Context, tx *sql.Tx, id string, result external.Result) error {
	// Invalid claims remain in the observation for diagnosis but cannot lower
	// recorded spending or be charged as known usage.
	if !result.ValidUsage() {
		return nil
	}
	u := result.Usage
	if u == nil {
		// Nonzero legacy fields remain compatible with other adapters. A
		// known zero must be explicit in Usage; absent fields mean unknown.
		u = &external.Usage{}
		if result.CostUSD != 0 {
			u.CostUSD = &result.CostUSD
		}
		if result.InputTokens != 0 {
			u.InputTokens = &result.InputTokens
		}
		if result.OutputTokens != 0 {
			u.OutputTokens = &result.OutputTokens
		}
	}
	if u.CostUSD == nil && u.InputTokens == nil && u.OutputTokens == nil {
		return nil
	}
	runID := result.References.RunID
	if runID == "" {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT references_json FROM external_attempts WHERE id=?`, id).Scan(&raw); err != nil {
			return err
		}
		var refs external.References
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &refs); err != nil {
				return err
			}
		}
		runID = refs.RunID
	}
	// Keep each execution's cumulative high-water mark. Repeated callbacks,
	// delayed older observations and missing metrics cannot reduce spending.
	_, err := tx.ExecContext(ctx, `INSERT INTO external_usage(attempt_id,run_id,cost_usd,input_tokens,output_tokens) VALUES(?,?,?,?,?)
 ON CONFLICT(attempt_id,run_id) DO UPDATE SET
 cost_usd=CASE WHEN excluded.cost_usd IS NULL THEN cost_usd ELSE MAX(COALESCE(cost_usd,0),excluded.cost_usd) END,
 input_tokens=CASE WHEN excluded.input_tokens IS NULL THEN input_tokens ELSE MAX(COALESCE(input_tokens,0),excluded.input_tokens) END,
 output_tokens=CASE WHEN excluded.output_tokens IS NULL THEN output_tokens ELSE MAX(COALESCE(output_tokens,0),excluded.output_tokens) END`, id, runID, u.CostUSD, u.InputTokens, u.OutputTokens)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE attempts SET cost_usd=(SELECT COALESCE(SUM(cost_usd),0) FROM external_usage WHERE attempt_id=?) WHERE id=(SELECT attempt_id FROM external_attempts WHERE id=?)`, id, id)
	return err
}
