package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// JSONScalar reads a top-level scalar property, or SQL NULL for missing, null or
// malformed JSON. expression and the simple property name key are trusted SQL
// inputs, never request values; key is not a nested path.
// PostgreSQL parses json, not jsonb, so reads don't impose jsonb's numeric limits.
func JSONScalar(q Execer, expression, key string) string {
	key = strings.ReplaceAll(key, "'", "''")
	if DialectOf(q) == Postgres {
		return "CASE WHEN pg_input_is_valid(" + expression + ", 'json') THEN json_extract_path_text(" + expression + "::json, '" + key + "') END"
	}
	return "CASE WHEN json_valid(" + expression + ") THEN json_extract(" + expression + ", '$." + key + "') END"
}

// JSONNumber is JSONScalar for numeric properties scanned as floating point.
func JSONNumber(q Execer, expression, key string) string {
	field := JSONScalar(q, expression, key)
	if DialectOf(q) == Postgres {
		return "CAST((" + field + ") AS double precision)"
	}
	return field
}

// JSONArrayLength returns zero for malformed JSON and non-array values, matching
// SQLite's json_array_length rather than PostgreSQL's error for non-arrays.
func JSONArrayLength(q Execer, expression string) string {
	if DialectOf(q) == Postgres {
		return "CASE WHEN pg_input_is_valid(" + expression + ", 'json') THEN CASE WHEN json_typeof(" + expression + "::json) = 'array' THEN json_array_length(" + expression + "::json) ELSE 0 END ELSE 0 END"
	}
	return "CASE WHEN json_valid(" + expression + ") THEN json_array_length(" + expression + ") ELSE 0 END"
}

// MutateJSONObject changes a top-level JSON object under the caller's writer
// transaction. Unknown fields remain raw JSON (including large integer values).
// Malformed/empty documents start as {}, while valid arrays/scalars remain
// untouched, as with SQLite json_set at an object property path.
// table and column are trusted schema identifiers, never request values.
func MutateJSONObject(ctx context.Context, tx *Transaction, table, column string, id int64, mutate func(map[string]json.RawMessage) error) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT `+column+` FROM `+table+` WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // match UPDATE of an absent row
	}
	if err != nil {
		return err
	}
	fields := map[string]json.RawMessage{}
	if json.Valid([]byte(raw)) {
		if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
			return nil
		}
	}
	if err := mutate(fields); err != nil {
		return err
	}
	updated, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE `+table+` SET `+column+` = ? WHERE id = ?`, string(updated), id)
	return err
}

// SetJSONFields preserves other properties without a lossy struct round-trip or
// jsonb conversion. WithTx must already hold the writer lock before the read.
func SetJSONFields(ctx context.Context, tx *Transaction, table, column string, id int64, values map[string]any) error {
	return MutateJSONObject(ctx, tx, table, column, id, func(fields map[string]json.RawMessage) error {
		for key, value := range values {
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			fields[key] = raw
		}
		return nil
	})
}
