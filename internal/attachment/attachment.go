// Package attachment builds the Content-Disposition header for a download.
package attachment

import "mime"

// Header is a Content-Disposition that saves the download as name, whatever name holds.
// Go's mime.FormatMediaType quotes it and escapes quotes and backslashes, and a name with
// non-ASCII or control characters (CR, LF) is encoded per RFC 2231, so no name can break
// the header or start another one. Should it ever still fail, the header is a bare
// "attachment" and the browser names the file from the address.
func Header(name string) string {
	if h := mime.FormatMediaType("attachment", map[string]string{"filename": name}); h != "" {
		return h
	}
	return "attachment"
}
