package applog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RewriteFiles passes every entry of the persisted log (the active file and its rotated
// history) through fn, keeping what it returns and dropping entries it rejects. It's for
// one-off clean-ups of lines already on disk, such as scrubbing details an older version
// should never have logged. Torn or malformed lines are dropped; entries fn hands back
// unchanged keep their exact original bytes.
//
// Each file is replaced atomically — written to a temp file beside it, synced, renamed
// over — so a crash part-way leaves either the old file or the new one, never half of
// each. Call it before Persist, while nothing is appending.
func RewriteFiles(path string, fn func(Entry) (Entry, bool)) error {
	files := []string{path}
	for i := 1; i <= FilesKept; i++ {
		files = append(files, rotatedName(path, i))
	}
	for _, name := range files {
		if !fileExists(name) {
			continue
		}
		if err := rewriteFile(name, fn); err != nil {
			return fmt.Errorf("rewrite %s: %w", filepath.Base(name), err)
		}
	}
	return nil
}

func rewriteFile(name string, fn func(Entry) (Entry, bool)) error {
	in, err := os.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".rewrite-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	out := bufio.NewWriterSize(tmp, 64<<10)
	// ReadBytes rather than a Scanner: one huge line must not end the rewrite early and
	// silently truncate the file.
	rd := bufio.NewReaderSize(in, 64<<10)
	for {
		line, readErr := rd.ReadBytes('\n')
		if raw := bytes.TrimRight(line, "\r\n"); len(raw) > 0 {
			if err := rewriteLine(out, raw, fn); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0o644) // CreateTemp makes it 0600; match what Persist creates
	if err := os.Rename(tmpName, name); err != nil {
		return err
	}
	done = true
	return nil
}

// rewriteLine writes one line's replacement, or nothing when the line is torn,
// malformed or rejected by fn.
func rewriteLine(out *bufio.Writer, raw []byte, fn func(Entry) (Entry, bool)) error {
	var e Entry
	if json.Unmarshal(raw, &e) != nil || e.TimeMS == 0 {
		return nil
	}
	next, keep := fn(e)
	if !keep {
		return nil
	}
	if next != e {
		b, err := json.Marshal(next)
		if err != nil {
			return err
		}
		raw = b
	}
	if _, err := out.Write(raw); err != nil {
		return err
	}
	return out.WriteByte('\n')
}
