package store

// This file interprets pg_restore's data-only output, never its SQL. The only
// database commands we execute are generated from trusted migration/catalog
// identifiers. In particular a custom archive cannot install functions, issue
// DDL, invoke setval on unrelated objects, or switch the connection's schema.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const archiveIdentifier = `(?:[a-z_][a-z0-9_]*|"[a-z_][a-z0-9_]*")`

var (
	archiveCopyHeader  = regexp.MustCompile(`^COPY (` + archiveIdentifier + `)\.(` + archiveIdentifier + `) \((` + archiveIdentifier + `(?:, ` + archiveIdentifier + `)*)\) FROM stdin;$`)
	archiveSetSequence = regexp.MustCompile(`^SELECT pg_catalog\.setval\('(` + archiveIdentifier + `)\.(` + archiveIdentifier + `)', ([0-9]+), (true|false)\);$`)
	archiveRestrict    = regexp.MustCompile(`^\\(?:un)?restrict [A-Za-z0-9]+$`)
)

type archiveSequence struct {
	value  int64
	called bool
}

type archiveDataReader struct {
	r         *bufio.Reader
	schema    string
	seen      map[string]bool
	sequences map[string]archiveSequence
}

func newArchiveDataReader(r io.Reader) *archiveDataReader {
	return &archiveDataReader{r: bufio.NewReaderSize(r, 64*1024), seen: map[string]bool{}, sequences: map[string]archiveSequence{}}
}

// next returns a table's COPY data as a bounded-memory stream. No untrusted
// string returned here may be executed as SQL; names must match the freshly
// initialized destination catalog before COPY is constructed.
func (r *archiveDataReader) next() (table string, columns []string, data *archiveCopyReader, err error) {
	for {
		line, readErr := r.r.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return "", nil, nil, errors.New("PostgreSQL archive contains an oversized statement")
		}
		if readErr != nil && readErr != io.EOF {
			return "", nil, nil, readErr
		}
		if len(line) == 0 && readErr == io.EOF {
			return "", nil, nil, io.EOF
		}
		s := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
		if s == "" || strings.HasPrefix(s, "--") || archiveRestrict.MatchString(s) || safeArchivePreamble(s) {
			continue
		}
		if match := archiveCopyHeader.FindStringSubmatch(s); match != nil {
			schema := strings.Trim(match[1], `"`)
			table := strings.Trim(match[2], `"`)
			if err := r.useSchema(schema); err != nil {
				return "", nil, nil, err
			}
			if r.seen[table] {
				return "", nil, nil, errors.New("PostgreSQL archive repeats table data")
			}
			r.seen[table] = true
			columns := strings.Split(match[3], ", ")
			for i := range columns {
				columns[i] = strings.Trim(columns[i], `"`)
			}
			return table, columns, &archiveCopyReader{r: r.r, lineStart: true, digest: newArchiveDigest()}, nil
		}
		if match := archiveSetSequence.FindStringSubmatch(s); match != nil {
			if err := r.useSchema(strings.Trim(match[1], `"`)); err != nil {
				return "", nil, nil, err
			}
			name := strings.Trim(match[2], `"`)
			if _, exists := r.sequences[name]; exists {
				return "", nil, nil, errors.New("PostgreSQL archive repeats a sequence")
			}
			value, err := strconv.ParseInt(match[3], 10, 64)
			if err != nil || value < 1 {
				return "", nil, nil, errors.New("PostgreSQL archive has an invalid sequence value")
			}
			r.sequences[name] = archiveSequence{value: value, called: match[4] == "true"}
			continue
		}
		return "", nil, nil, errors.New("PostgreSQL archive contains unsupported data statements; only Arrmada native COPY archives are supported")
	}
}

func (r *archiveDataReader) useSchema(schema string) error {
	if !postgresSchemaName.MatchString(schema) || schema == "public" || schema == "information_schema" || strings.HasPrefix(schema, "pg_") {
		return errors.New("PostgreSQL archive must contain one designated Arrmada schema")
	}
	if r.schema != "" && r.schema != schema {
		return errors.New("PostgreSQL archive includes multiple schemas")
	}
	r.schema = schema
	return nil
}

func safeArchivePreamble(s string) bool {
	// These lines are recognized and ignored, NEVER executed. Connection
	// settings for the import are controlled by the trusted connector below.
	switch s {
	case "SET statement_timeout = 0;", "SET lock_timeout = 0;",
		"SET idle_in_transaction_session_timeout = 0;", "SET transaction_timeout = 0;",
		"SET client_encoding = 'UTF8';", "SET standard_conforming_strings = on;",
		"SELECT pg_catalog.set_config('search_path', '', false);",
		"SET check_function_bodies = false;", "SET xmloption = content;",
		"SET client_min_messages = warning;", "SET row_security = off;",
		"SET default_tablespace = '';", "SET default_table_access_method = heap;":
		return true
	}
	return false
}

type archiveCopyReader struct {
	r          *bufio.Reader
	pending    []byte
	lineStart  bool
	terminated bool
	err        error
	digest     *archiveDigest
}

func (r *archiveCopyReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) != 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.terminated {
		return 0, io.EOF
	}
	if r.lineStart {
		marker, _ := r.r.Peek(3)
		if bytes.Equal(marker, []byte("\\.\n")) {
			_, _ = r.r.Discard(3)
			r.terminated = true
			return 0, io.EOF
		}
	}
	chunk, err := r.r.ReadSlice('\n')
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
		r.err = errors.New("PostgreSQL archive COPY data is truncated")
		return 0, r.err
	}
	r.lineStart = err == nil
	_, _ = r.digest.Write(chunk)
	n := copy(p, chunk)
	r.pending = chunk[n:]
	return n, nil
}

// archiveDigest fingerprints the multiset of canonical COPY rows, independent
// of scan order. Each row is SHA-256 hashed, then added modulo 2^256. This keeps
// memory bounded even for very large tables; unlike XOR, duplicate row pairs do
// not trivially cancel. Row count is compared separately as well.
type archiveDigest struct {
	row     hash.Hash
	sum     [sha256.Size]byte
	rows    int64
	partial bool
}

func newArchiveDigest() *archiveDigest { return &archiveDigest{row: sha256.New()} }

func (d *archiveDigest) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) != 0 {
		at := bytes.IndexByte(p, '\n')
		if at < 0 {
			_, _ = d.row.Write(p)
			d.partial = true
			break
		}
		_, _ = d.row.Write(p[:at+1])
		h := d.row.Sum(nil)
		carry := uint16(0)
		for i := len(d.sum) - 1; i >= 0; i-- {
			carry += uint16(d.sum[i]) + uint16(h[i])
			d.sum[i] = byte(carry)
			carry >>= 8
		}
		d.rows++
		d.partial = false
		d.row.Reset()
		p = p[at+1:]
	}
	return n, nil
}
