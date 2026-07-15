package download

import "testing"

const qbitInfoSample = `[
  {"hash":"aaa","name":"Dune.Part.Two.2024.2160p","size":25769803776,"progress":0.62,
   "dlspeed":19283712,"upspeed":0,"eta":240,"state":"downloading","ratio":0.0,
   "category":"arrmada","completed":15977221324,"amount_left":9792582452},
  {"hash":"bbb","name":"Shogun.S01","size":19000000000,"progress":1.0,
   "dlspeed":0,"upspeed":3355443,"eta":8640000,"state":"stalledUP","ratio":2.41,
   "category":"arrmada","completed":19000000000,"amount_left":0}
]`

func TestParseTorrentsInfo(t *testing.T) {
	items, err := parseTorrentsInfo([]byte(qbitInfoSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	d := items[0]
	if d.State != "downloading" {
		t.Errorf("state = %q, want downloading", d.State)
	}
	if d.Progress != 0.62 || d.DownSpeed != 19283712 || d.ETASeconds != 240 {
		t.Errorf("unexpected download fields: %+v", d)
	}
	if d.DownloadedBytes != 15977221324 || d.SizeBytes != 25769803776 {
		t.Errorf("bytes = %d/%d", d.DownloadedBytes, d.SizeBytes)
	}

	// A stalledUP torrent normalizes to "seeding".
	if items[1].State != "seeding" {
		t.Errorf("state[1] = %q, want seeding", items[1].State)
	}
	if items[1].Ratio != 2.41 {
		t.Errorf("ratio = %v", items[1].Ratio)
	}
}

func TestNormalizeState(t *testing.T) {
	cases := map[string]string{
		"downloading": "downloading",
		"metaDL":      "downloading",
		"stalledDL":   "downloading",
		"uploading":   "seeding",
		"stalledUP":   "seeding",
		"pausedDL":    "paused",
		"error":       "error",
		"moving":      "checking",
	}
	for in, want := range cases {
		if got := normalizeState(in); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", in, got, want)
		}
	}
}
