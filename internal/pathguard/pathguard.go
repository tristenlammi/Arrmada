// Package pathguard answers one question safely: is this path really inside one of
// these folders?
//
// Comparing strings isn't enough. "/library-old" starts with "/library", "a/../.." walks
// out, and a symlink inside the downloads folder can point at /etc or the whole array.
// Every check here works on fully resolved paths — cleaned, absolute, symlinks followed —
// and compares on a separator boundary.
package pathguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxLinkHops bounds symlink chains so a link loop can't spin forever.
const maxLinkHops = 40

// Resolve cleans p, makes it absolute and follows every symlink in it. A path that
// doesn't exist yet (an import target, a folder about to be created) still resolves:
// the deepest part that exists is resolved and the missing tail is rejoined, so a
// symlinked parent can't hide behind a missing child.
func Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	return resolve(p, 0)
}

func resolve(p string, hops int) (string, error) {
	if hops > maxLinkHops {
		return "", errors.New("too many symlinks")
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	var tail []string // missing elements, deepest first
	cur := abs
	for {
		found, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				found = filepath.Join(found, tail[i])
			}
			return found, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// A dangling symlink "doesn't exist" to EvalSymlinks, but its target is exactly
		// where a later write would land, so follow it by hand.
		if fi, lerr := os.Lstat(cur); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			target, rerr := os.Readlink(cur)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			for i := len(tail) - 1; i >= 0; i-- {
				target = filepath.Join(target, tail[i])
			}
			return resolve(target, hops+1)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Nothing on the way up exists (not even the volume root): nothing to follow.
			return abs, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// Within reports whether p is one of roots or inside one. Both sides are resolved, so
// symlinks and ".." can't fake it, and the comparison is on a separator boundary, so
// "/library-old" is not inside "/library". Empty roots are skipped; a path that can't
// be resolved is never within anything.
func Within(p string, roots ...string) bool {
	rp, err := Resolve(p)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		rr, err := Resolve(root)
		if err != nil {
			continue
		}
		if contains(rr, rp) {
			return true
		}
	}
	return false
}

// Under reports whether p is dir or inside it. An empty dir contains nothing.
func Under(p, dir string) bool {
	return Within(p, dir)
}

// contains compares two already-resolved paths.
func contains(root, p string) bool {
	if p == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}
