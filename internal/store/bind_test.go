package store

import "testing"

func TestBindParameters(t *testing.T) {
	tests := []struct {
		name, query, want string
	}{
		{"parameters", `SELECT ?::text, ?::bigint`, `SELECT $1::text, $2::bigint`},
		{"numbered parameters", `SELECT ?2, ?1, ?2`, `SELECT $2, $1, $2`},
		{"mixed numbered and bare", `SELECT ?2, ?, ?1, ?`, `SELECT $2, $3, $1, $4`},
		{"bare before numbered", `SELECT ?, ?1, ?3, ?`, `SELECT $1, $1, $3, $4`},
		{"leading zero index", `SELECT ?0002, ?`, `SELECT $2, $3`},
		{"zero index stays invalid", `SELECT ?0`, `SELECT $0`},
		{"overflow index stays invalid", `SELECT ?999999999999999999999999`, `SELECT $999999999999999999999999`},
		{"numbered in literals", `SELECT '?2', "?4", $$?3$$, ?1 /* ?9 */`, `SELECT '?2', "?4", $$?3$$, $1 /* ?9 */`},
		{"quoted", `SELECT '?', 'isn''t ?', "?", "a""?", ?`, `SELECT '?', 'isn''t ?', "?", "a""?", $1`},
		{"escape string", `SELECT E'it\'s ?', ?`, `SELECT E'it\'s ?', $1`},
		{"standard string", `SELECT '\', ?`, `SELECT '\', $1`},
		{"dollar quotes", `SELECT $$?$$, $body_1$? '$body_1$, ?`, `SELECT $$?$$, $body_1$? '$body_1$, $1`},
		{"unicode dollar tag", `SELECT $é$?$é$, ?`, `SELECT $é$?$é$, $1`},
		{"identifier dollar", `SELECT name$tag$, ?`, `SELECT name$tag$, $1`},
		{"comments", "SELECT ? /* ? /* ? */ ? */ -- ?\n, ?", "SELECT $1 /* ? /* ? */ ? */ -- ?\n, $2"},
		{"line comment EOF", `SELECT ? -- ?`, `SELECT $1 -- ?`},
		{"CR line ending", "SELECT ? -- ?\r, ?", "SELECT $1 -- ?\r, $2"},
		{"operators", `SELECT doc ?? ?, doc ??| array[?], doc ??& array[?]`, `SELECT doc ? $1, doc ?| array[$2], doc ?& array[$3]`},
		{"many parameters", `SELECT ?,?,?,?,?,?,?,?,?,?,?`, `SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11`},
		{"native parameters", `SELECT $1, '$2?'`, `SELECT $1, '$2?'`},
		{"native followed by bare", `SELECT $2, ?, ?1`, `SELECT $2, $3, $1`},
		{"identifier dollar number", `SELECT name$9, ?`, `SELECT name$9, $1`},
		{"unterminated string", `SELECT '?, ?`, `SELECT '?, ?`},
		{"unterminated dollar", `SELECT $tag$?, ?`, `SELECT $tag$?, ?`},
		{"unterminated comment", `SELECT /* ?, ?`, `SELECT /* ?, ?`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Postgres.bind(tt.query); got != tt.want {
				t.Errorf("Postgres.bind(%q) = %q, want %q", tt.query, got, tt.want)
			}
			if got := SQLite.bind(tt.query); got != tt.query {
				t.Errorf("SQLite changed query: %q", got)
			}
		})
	}
}

func TestBindLeavesPortableSQLUntouched(t *testing.T) {
	query := `INSERT INTO users (username) VALUES (?) ON CONFLICT DO NOTHING RETURNING id`
	if got := Postgres.bind(query); got != `INSERT INTO users (username) VALUES ($1) ON CONFLICT DO NOTHING RETURNING id` {
		t.Fatal(got)
	}
}
