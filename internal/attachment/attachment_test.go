package attachment

import (
	"mime"
	"strings"
	"testing"
)

// Any name makes one well-formed header that reads back as the same name: quotes, a
// semicolon, CR/LF and non-ASCII can't end the value early or start another header.
func TestHeaderSurvivesAnyName(t *testing.T) {
	for _, name := range []string{
		"plain.mkv",
		`Les Misérables - "Victor" Hugo.epub`,
		`a"; filename="evil.exe`,
		"two\r\nSet-Cookie: x=1.gz",
		`back\slash; semi;colon "quoted".mp4`,
		"東京物語 (1953).mkv",
	} {
		h := Header(name)
		if strings.ContainsAny(h, "\r\n") {
			t.Errorf("%q: header carries a line break: %q", name, h)
			continue
		}
		disp, params, err := mime.ParseMediaType(h)
		if err != nil || disp != "attachment" {
			t.Errorf("%q: header %q doesn't parse: %v", name, h, err)
			continue
		}
		if params["filename"] != name {
			t.Errorf("%q: header %q reads back as %q", name, h, params["filename"])
		}
		if len(params) != 1 {
			t.Errorf("%q: header %q has extra parameters %v", name, h, params)
		}
	}
}
