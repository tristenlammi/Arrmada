package main

import (
	"strings"
	"testing"
)

func TestConversionCLIRequiresExplicitDirectionAndDestination(t *testing.T) {
	t.Setenv("ARRMADA_DATA_DIR", t.TempDir())
	t.Setenv("ARRMADA_DATABASE_BACKEND", "sqlite")
	t.Setenv("ARRMADA_POSTGRES_URL", "")
	t.Setenv("ARRMADA_POSTGRES_SCHEMA", "")
	for _, args := range [][]string{
		{"convert-database"},
		{"convert-database", "--from", "sqlite", "--to", "sqlite", "--sqlite-file", "file.db"},
		{"convert-database", "--from", "sqlite", "--to", "postgres"},
		{"convert-database", "--from", "sqlite", "--to", "postgres", "--sqlite-file", "file.db"},
	} {
		if code, _, output := runTestCLI(t, "", args...); code != exitUsage {
			t.Fatalf("conversion usage: exit %d %s", code, output)
		}
	}
	t.Setenv("ARRMADA_POSTGRES_URL", "postgres://app:never-log-this@localhost/db?sslmode=invalid")
	t.Setenv("ARRMADA_POSTGRES_SCHEMA", "arrmada")
	code, out, err := runTestCLI(t, "", "convert-database", "--from", "sqlite", "--to", "postgres", "--sqlite-file", "missing-source.db")
	if code != exitFail || strings.Contains(out+err, "never-log-this") {
		t.Fatalf("conversion leaked secret or accepted missing source: %d %s%s", code, out, err)
	}
}
