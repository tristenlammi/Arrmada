// Command postgres-baseline inspects a fresh, fully migrated SQLite database.
// It is a development aid for reviewing the PostgreSQL baseline, never a runtime
// migration or a converter for an existing installation.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/tristenlammi/arrmada/internal/store"
)

func main() {
	postgres := flag.Bool("postgres", false, "emit PostgreSQL baseline SQL instead of the SQLite object inventory")
	flag.Parse()
	if err := inspect(*postgres); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(postgres bool) error {
	dir, err := os.MkdirTemp("", "arrmada-schema-inspect-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if postgres {
		return emitPostgres(os.Stdout, st.DB())
	}
	rows, err := st.DB().Query(`SELECT type, name, tbl_name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type object struct{ Type, Name, Table, SQL string }
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.Type, &o.Name, &o.Table, &o.SQL); err != nil {
			return err
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Version string
		Objects []object
	}{store.LatestMigration(), objects})
}
