package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot is the module root, found from this file's location so the test works from
// any working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("can't locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// parseDir parses every non-test .go file directly in dir.
func parseDir(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = f
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s — has the package moved?", dir)
	}
	return fset, files
}

// TestNoNakedGoroutines: HTTP handlers and main start background work through the run
// group (a.bg, grp.Go, grp.Loop), never a bare go statement. A bare one escapes both the
// panic recovery — one nil dereference in it restarts the whole app mid-encode — and the
// shutdown that cancels and waits for background work.
func TestNoNakedGoroutines(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"internal/httpapi", "cmd/arrmada"} {
		fset, files := parseDir(t, filepath.Join(root, filepath.FromSlash(rel)))
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				if g, ok := n.(*ast.GoStmt); ok {
					pos := fset.Position(g.Pos())
					t.Errorf("%s:%d: go statement in %s — use a.bg (handlers) or grp.Go/grp.Loop (main) so a panic is contained and shutdown can stop it",
						filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator))), pos.Line, rel)
				}
				return true
			})
		}
	}
}
