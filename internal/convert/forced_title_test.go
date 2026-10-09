package convert

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A subtitle stream whose title says "Forced" is forced even without the disposition
// flag. The "ffprobe" here is a shell script printing a synthetic answer — no media.
func TestProbeTitleMarksForced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell for the fake ffprobe")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffprobe")
	script := `#!/bin/sh
cat <<'JSON'
{"streams":[
 {"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","r_frame_rate":"24/1","avg_frame_rate":"24/1"},
 {"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng","title":"English (Forced)"}},
 {"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","tags":{"language":"eng","title":"English"}},
 {"codec_type":"subtitle","codec_name":"subrip","disposition":{"forced":1},"tags":{"language":"eng"}}
],"format":{"format_name":"matroska,webm","duration":"60","size":"1000","bit_rate":"1000"}}
JSON
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mi, err := probe(context.Background(), fake, filepath.Join(dir, "x.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.Subs) != 3 {
		t.Fatalf("subs = %d, want 3", len(mi.Subs))
	}
	if !mi.Subs[0].Forced || mi.Subs[0].Title != "English (Forced)" {
		t.Errorf("titled forced track = %+v, want Forced and its title", mi.Subs[0])
	}
	if mi.Subs[1].Forced {
		t.Errorf("plain English track marked forced: %+v", mi.Subs[1])
	}
	if !mi.Subs[2].Forced {
		t.Errorf("disposition-forced track lost its flag: %+v", mi.Subs[2])
	}
}
