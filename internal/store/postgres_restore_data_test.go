package store

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestArchiveDataReaderRejectsUntrustedSQL(t *testing.T) {
	for _, input := range []string{
		"SELECT evil();\n",
		"SET search_path = public;\n",
		"ALTER TABLE arrmada.settings DISABLE TRIGGER ALL;\n",
		"COPY public.settings (key, value) FROM stdin;\n\\.\n",
		"COPY arrmada.settings (key, value) FROM PROGRAM 'evil';\n",
		"COPY arrmada.settings (key, value) FROM stdin;\n\\.\nCOPY arrmada.settings (key, value) FROM stdin;\n\\.\n",
		"COPY arrmada.settings (key, value) FROM stdin;\n\\.\nCOPY foreign.settings (key, value) FROM stdin;\n\\.\n",
		"SELECT pg_catalog.setval('arrmada.users_id_seq', 9223372036854775808, true);\n",
		"SELECT pg_catalog.setval('arrmada.users_id_seq', 1, true);\nSELECT pg_catalog.setval('arrmada.users_id_seq', 2, true);\n",
		strings.Repeat("x", 64*1024) + "\n",
	} {
		r := newArchiveDataReader(strings.NewReader(input))
		rejected := false
		for {
			_, _, data, err := r.next()
			if err != nil {
				rejected = err != io.EOF
				break
			}
			if _, err := io.Copy(io.Discard, data); err != nil {
				rejected = true
				break
			}
		}
		if !rejected {
			t.Errorf("accepted unsafe or malformed archive input of length %d", len(input))
		}
	}
}

func TestArchiveCopyReaderStreamsLargeRowsAndDetectsTruncation(t *testing.T) {
	row := strings.Repeat("large-data", 20000) + "\t\\N\t\\\\.\n"
	input := "COPY \"arrmada\".\"settings\" (\"key\", value) FROM stdin;\n" + row + "\\.\nSELECT pg_catalog.setval('arrmada.users_id_seq', 900, true);\n"
	r := newArchiveDataReader(strings.NewReader(input))
	name, cols, data, err := r.next()
	if err != nil || name != "settings" || strings.Join(cols, ",") != "key,value" {
		t.Fatalf("COPY header = %q, %v, %v", name, cols, err)
	}
	var out bytes.Buffer
	buf := make([]byte, 7)
	if _, err := io.CopyBuffer(&out, data, buf); err != nil || out.String() != row || data.digest.rows != 1 || !data.terminated || data.digest.partial {
		t.Fatalf("large COPY row streaming failed: %v", err)
	}
	if _, _, _, err := r.next(); err != io.EOF || r.sequences["users_id_seq"].value != 900 {
		t.Fatalf("sequence after COPY was lost: %v", err)
	}
	for _, fragment := range []string{"", "data", "data\n"} {
		r := newArchiveDataReader(strings.NewReader("COPY arrmada.settings (key, value) FROM stdin;\n" + fragment))
		_, _, data, err := r.next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, data); err == nil || data.terminated {
			t.Fatal("truncated COPY data accepted")
		}
	}
}

func TestArchiveDigestComparesMultisetsWithoutDuplicateCancellation(t *testing.T) {
	one, two, different := newArchiveDigest(), newArchiveDigest(), newArchiveDigest()
	_, _ = one.Write([]byte("one\none\ntwo\n"))
	for _, part := range []string{"tw", "o\non", "e\none\n"} {
		_, _ = two.Write([]byte(part))
	}
	_, _ = different.Write([]byte("other\nother\ntwo\n"))
	if one.sum != two.sum || one.rows != two.rows || two.partial || one.sum == different.sum {
		t.Fatal("row fingerprints do not distinguish the transferred multisets")
	}
}

func FuzzArchiveDataReader(f *testing.F) {
	f.Add("COPY arrmada.settings (key, value) FROM stdin;\nkey\tvalue\n\\.\n")
	f.Add("SELECT pg_catalog.setval('arrmada.users_id_seq', 100, true);\n")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1024*1024 {
			t.Skip()
		}
		r := newArchiveDataReader(strings.NewReader(input))
		for {
			_, _, data, err := r.next()
			if err != nil {
				return
			}
			if _, err := io.Copy(io.Discard, data); err != nil {
				return
			}
		}
	})
}
