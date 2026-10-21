package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/store"
)

func cmdConvertDatabase(ctx context.Context, c *cli, args []string) int {
	flags := newFlags(c, "convert-database")
	from := flags.String("from", "", "source backend: sqlite or postgres")
	to := flags.String("to", "", "destination backend: postgres or sqlite")
	file := flags.String("sqlite-file", "", "explicit SQLite source or new destination file (required)")
	pos, stop, code := parseFlags(flags, args)
	if stop {
		return code
	}
	if len(pos) != 0 || *file == "" || !((*from == "sqlite" && *to == "postgres") || (*from == "postgres" && *to == "sqlite")) {
		fmt.Fprintln(c.stderr, "Usage: arrmada convert-database --from sqlite|postgres --to postgres|sqlite --sqlite-file <source or new destination.db>")
		return exitUsage
	}
	if c.cfg.PostgresURL == "" || c.cfg.PostgresSchema == "" {
		fmt.Fprintln(c.stderr, "arrmada convert-database: set ARRMADA_POSTGRES_URL and ARRMADA_POSTGRES_SCHEMA; credentials must not be command-line arguments")
		return exitUsage
	}
	opt := postgresOptions(c.cfg)
	var err error
	if *from == "sqlite" {
		err = store.ConvertSQLiteToPostgres(ctx, *file, opt, filepath.Join(c.cfg.DataDir, "conversion-work"))
	} else {
		err = store.ConvertPostgresToSQLite(ctx, opt, *file)
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "arrmada convert-database:", err)
		return exitFail
	}
	fmt.Fprintln(c.stdout, "Offline conversion verified and committed. Source preserved; runtime configuration was not changed. Keep Arrmada stopped until the backend/schema and filesystem paths have been checked.")
	return exitOK
}
