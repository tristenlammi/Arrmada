package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// A conversion pair is reviewed explicitly, not inferred from coincidentally
// similar migration numbers. Future migrations must update this contract and
// its paired integration tests before conversion is enabled for that build.
const conversionSQLiteVersion = "0347_quality_multi_counts_language"
const conversionPostgresVersion = "0004_subtitle_job_ids"

var ErrConversionIncompatible = errors.New("database is incompatible with this build's offline conversion pair")

type conversionColumn struct {
	name, kind string
	pk         int
}
type conversionTable struct {
	name     string
	columns  []conversionColumn
	identity string
}
type conversionObject struct{ kind, name, table, sql string }
type conversionModel struct {
	tables  []conversionTable
	objects []conversionObject
}

func conversionReference(ctx context.Context, workDir string) (*Store, *conversionModel, func(), error) {
	if LatestMigration() != conversionSQLiteVersion {
		return nil, nil, nil, ErrConversionIncompatible
	}
	if err := mkPrivateDir(workDir); err != nil {
		return nil, nil, nil, err
	}
	if err := conversionDiskSpace(workDir, nil); err != nil {
		return nil, nil, nil, err
	}
	dir, err := os.MkdirTemp(workDir, ".arrmada-conversion-*")
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) } // Only this newly created private directory.
	st, err := Open(dir)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	model, err := sqliteConversionModel(ctx, st.db)
	if err != nil {
		_ = st.Close()
		cleanup()
		return nil, nil, nil, err
	}
	return st, model, cleanup, nil
}

func sqliteConversionModel(ctx context.Context, q Execer) (*conversionModel, error) {
	m := &conversionModel{}
	rows, err := q.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master
		WHERE name NOT GLOB 'sqlite_*' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o conversionObject
		if err := rows.Scan(&o.kind, &o.name, &o.table, &o.sql); err != nil {
			rows.Close()
			return nil, err
		}
		m.objects = append(m.objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, object := range m.objects {
		if object.kind != "table" || object.name == "schema_migrations" {
			continue
		}
		t := conversionTable{name: object.name}
		cols, err := q.QueryContext(ctx, `PRAGMA table_info(`+pgx.Identifier{t.name}.Sanitize()+`)`)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c conversionColumn
			var cid, nn int
			var def sql.NullString
			if err := cols.Scan(&cid, &c.name, &c.kind, &nn, &def, &c.pk); err != nil {
				cols.Close()
				return nil, err
			}
			if _, ok := conversionPGTypes[c.kind]; !ok {
				cols.Close()
				return nil, ErrConversionIncompatible
			}
			t.columns = append(t.columns, c)
		}
		err = cols.Err()
		cols.Close()
		if err != nil {
			return nil, err
		}
		pkCount := 0
		for _, c := range t.columns {
			if c.pk != 0 {
				pkCount++
			}
		}
		for _, c := range t.columns {
			if c.pk == 1 && c.kind == "INTEGER" && pkCount == 1 {
				t.identity = c.name
			}
		}
		m.tables = append(m.tables, t)
	}
	sort.Slice(m.tables, func(i, j int) bool { return m.tables[i].name < m.tables[j].name })
	return m, nil
}

var conversionPGTypes = map[string]string{"INTEGER": "int8", "TEXT": "text", "REAL": "float8", "BLOB": "bytea", "TIMESTAMP": "timestamp"}

func (t conversionTable) names() []string {
	names := make([]string, len(t.columns))
	for i, c := range t.columns {
		names[i] = c.name
	}
	return names
}

func requireSQLiteConversionSchema(ctx context.Context, tx *sql.Tx, want *conversionModel) error {
	names, err := listMigrations(embeddedMigrations())
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, n := range names {
		expected[strings.TrimSuffix(n, ".sql")] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return ErrConversionIncompatible
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return ErrConversionIncompatible
		}
		if !expected[name] {
			rows.Close()
			return ErrConversionIncompatible
		}
		delete(expected, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(expected) != 0 {
		return ErrConversionIncompatible
	}
	actual, err := sqliteConversionModel(ctx, tx)
	if err != nil || !reflect.DeepEqual(actual, want) {
		return ErrConversionIncompatible
	}
	return sqliteConversionIntegrity(ctx, tx)
}

func sqliteConversionIntegrity(ctx context.Context, q Execer) error {
	var result string
	if err := q.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		return errors.New("SQLite conversion database failed integrity checking")
	}
	rows, err := q.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	bad := rows.Next()
	err = rows.Err()
	rows.Close()
	if bad || err != nil {
		return errors.New("SQLite conversion database has invalid relationships")
	}
	return nil
}

func requirePostgresConversionSchema(ctx context.Context, tx pgx.Tx, model *conversionModel) (map[string]recoverySequence, error) {
	if err := verifyRecoveryMigrations(ctx, tx); err != nil {
		return nil, ErrConversionIncompatible
	}
	var latest string
	if err := tx.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != conversionPostgresVersion {
		return nil, ErrConversionIncompatible
	}
	tables, seqs, err := recoveryCatalog(ctx, tx)
	if err != nil || len(tables) != len(model.tables)+1 {
		return nil, ErrConversionIncompatible
	}
	identities := map[string]string{}
	for _, table := range model.tables {
		if table.identity != "" {
			identities[table.name] = table.identity
		}
	}
	if len(seqs) != len(identities) {
		return nil, ErrConversionIncompatible
	}
	seenIdentities := map[string]bool{}
	for _, seq := range seqs {
		if seenIdentities[seq.table] || identities[seq.table] != seq.column {
			return nil, ErrConversionIncompatible
		}
		seenIdentities[seq.table] = true
	}
	// No untransferred data-bearing relations or unowned sequence state may
	// hide outside the ordinary-table/identity catalog used below.
	var unexpected bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=current_schema() AND (c.relkind NOT IN ('r','i','S') OR c.relrowsecurity OR c.relforcerowsecurity OR
		EXISTS (SELECT 1 FROM pg_inherits h WHERE h.inhrelid=c.oid OR h.inhparent=c.oid) OR
		(c.relkind='S' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype IN ('a','i')))))`).Scan(&unexpected); err != nil || unexpected {
		return nil, ErrConversionIncompatible
	}
	var tableCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind='r'`).Scan(&tableCount); err != nil || tableCount != len(model.tables)+1 {
		return nil, ErrConversionIncompatible
	}
	for _, table := range model.tables {
		if tables[table.name] == nil || !reflect.DeepEqual(tables[table.name].columns, table.names()) {
			return nil, ErrConversionIncompatible
		}
		rows, err := tx.Query(ctx, `SELECT a.attname, y.typname FROM pg_attribute a JOIN pg_type y ON y.oid = a.atttypid
			WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, pgx.Identifier{table.name}.Sanitize())
		if err != nil {
			return nil, ErrConversionIncompatible
		}
		i := 0
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil || i >= len(table.columns) || name != table.columns[i].name || typ != conversionPGTypes[table.columns[i].kind] {
				rows.Close()
				return nil, ErrConversionIncompatible
			}
			i++
		}
		err = rows.Err()
		rows.Close()
		if err != nil || i != len(table.columns) {
			return nil, ErrConversionIncompatible
		}
	}
	// All compatibility identities have the baseline's increment/min/max/cache
	// contract; cycling or hand-retuned sequences cannot be translated safely.
	var bad bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND (s.seqincrement <> 1 OR s.seqmin <> 1 OR s.seqmax <> 9223372036854775807 OR s.seqcache <> 1 OR s.seqcycle))`).Scan(&bad); err != nil || bad {
		return nil, ErrConversionIncompatible
	}
	return seqs, nil
}

// normalizeConversionValue is deliberately strict about SQLite's dynamic value
// types and PostgreSQL-only values. Never coerce a numeric string, invalid UTF8,
// NUL text, nanosecond timestamp or non-finite float into something "close".
func normalizeConversionValue(column conversionColumn, value any, toSQLite bool) (any, error) {
	if value == nil {
		if column.pk != 0 {
			return nil, fmt.Errorf("primary-key column %s contains NULL", column.name)
		}
		return nil, nil
	}
	bad := func() (any, error) {
		return nil, fmt.Errorf("unsupported conversion value in column %s (%s); value omitted", column.name, column.kind)
	}
	switch column.kind {
	case "INTEGER":
		if v, ok := value.(int64); ok {
			return v, nil
		}
	case "TEXT":
		if v, ok := value.(string); ok && utf8.ValidString(v) && !strings.ContainsRune(v, 0) {
			return v, nil
		}
	case "BLOB":
		if v, ok := value.([]byte); ok {
			out := make([]byte, len(v))
			copy(out, v)
			return out, nil
		}
	case "REAL":
		var v float64
		switch n := value.(type) {
		case float64:
			v = n
		case int64:
			v = float64(n)
			if v < math.MinInt64 || v >= 9223372036854775808.0 || int64(v) != n {
				return bad()
			}
		default:
			return bad()
		}
		if !math.IsNaN(v) && !math.IsInf(v, 0) && !(toSQLite && v == 0 && math.Signbit(v)) {
			return v, nil
		}
	case "TIMESTAMP":
		if v, ok := value.(time.Time); ok {
			v = v.UTC()
			if v.Year() >= 1 && v.Year() <= 9999 && v.Nanosecond()%1000 == 0 {
				return v, nil
			}
		}
	}
	return bad()
}

func conversionValues(table conversionTable, values []any, toSQLite bool) ([]any, error) {
	if len(values) != len(table.columns) {
		return nil, ErrConversionIncompatible
	}
	out := make([]any, len(values))
	for i, c := range table.columns {
		v, err := normalizeConversionValue(c, values[i], toSQLite)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", table.name, err)
		}
		out[i] = v
	}
	return out, nil
}

type conversionDigest struct {
	sum  [32]byte
	rows int64
}

func (d *conversionDigest) add(values []any) {
	h := sha256.New()
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			_, _ = h.Write([]byte{0})
		case int64:
			conversionHashBytes(h, 1, uint64(v))
		case float64:
			conversionHashBytes(h, 2, math.Float64bits(v))
		case string:
			conversionHashBytes(h, 3, uint64(len(v)))
			_, _ = h.Write([]byte(v))
		case []byte:
			conversionHashBytes(h, 4, uint64(len(v)))
			_, _ = h.Write(v)
		case time.Time:
			conversionHashBytes(h, 5, uint64(v.UnixMicro()))
		default:
			panic("unvalidated conversion value")
		}
	}
	row := h.Sum(nil)
	carry := uint16(0)
	for i := len(d.sum) - 1; i >= 0; i-- {
		carry += uint16(d.sum[i]) + uint16(row[i])
		d.sum[i] = byte(carry)
		carry >>= 8
	}
	d.rows++
}

func conversionHashBytes(h hash.Hash, tag byte, value uint64) {
	var b [9]byte
	b[0] = tag
	binary.BigEndian.PutUint64(b[1:], value)
	_, _ = h.Write(b[:])
}

func conversionSQLiteHighWater(ctx context.Context, q Execer, model *conversionModel) (map[string]int64, error) {
	allowed := map[string]bool{}
	for _, t := range model.tables {
		if t.identity != "" {
			allowed[t.name] = true
		}
	}
	states := map[string]int64{}
	rows, err := q.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var raw any
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		n, ok := raw.(int64)
		if _, duplicate := states[name]; duplicate || !allowed[name] || !ok || n < 0 {
			return nil, errors.New("SQLite identity high-water metadata is incompatible")
		}
		states[name] = n
	}
	return states, rows.Err()
}

func conversionMaxIdentity(table conversionTable, values []any, high map[string]int64) {
	for i, c := range table.columns {
		if c.name == table.identity {
			if n, ok := values[i].(int64); ok && n > high[table.name] {
				high[table.name] = n
			}
		}
	}
}

func conversionDiskSpace(dir string, row []any) error {
	need := uint64(postgresBackupReserve + 1024*1024)
	for _, v := range row {
		switch n := v.(type) {
		case string:
			need += uint64(len(n)) * 2
		case []byte:
			need += uint64(len(n)) * 2
		}
	}
	if u, ok := freeSpace(dir); ok && u.FreeBytes < need {
		return ErrNoSpace
	}
	return nil
}

func conversionDestinationFree(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return errors.New("SQLite conversion destination is occupied or cannot be inspected")
		}
	}
	if filepath.Base(path) == "arrmada.db" {
		if marker, err := PendingRestore(filepath.Dir(path)); err != nil || marker != nil {
			return errors.New("SQLite conversion destination has a staged restore")
		}
	}
	return nil
}
