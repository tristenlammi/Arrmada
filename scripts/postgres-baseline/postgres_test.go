package main

import (
	"reflect"
	"testing"
)

func TestCheckExpressions(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{`CREATE TABLE x (status TEXT CHECK (status IN ('open', 'ignored')), CHECK (a < b))`, []string{`status IN ('open', 'ignored')`, `a < b`}},
		{"CREATE TABLE x (check_at TEXT, value TEXT DEFAULT 'CHECK (not a check)' -- CHECK (ignored)\n, CHECK (value != 'don''t)'))", []string{`value != 'don''t)'`}},
		{`/* CHECK (ignored) */ CREATE TABLE x ("CHECK" TEXT, CHECK ((a + 1) > 0))`, []string{`(a + 1) > 0`}},
		{`CREATE TABLE x (value TEXT)`, nil},
	}
	for _, tt := range tests {
		got, err := checkExpressions(tt.query)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("checkExpressions(%q) = %v, %v; want %v", tt.query, got, err, tt.want)
		}
	}
	if _, err := checkExpressions(`CHECK (unterminated`); err == nil {
		t.Fatal("accepted incomplete check")
	}
}
