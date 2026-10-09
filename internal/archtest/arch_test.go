package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// sourceDirs lists every directory under internal/ and cmd/ that holds Go files.
func sourceDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if name := d.Name(); name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
					dirs = append(dirs, p)
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	return dirs
}

// TestTransactionsGoThroughWithTx: multi-step writes use store.WithTx, never a hand-rolled
// BeginTx. WithTx retries a busy BEGIN and rolls back on an error or a panic; the
// boilerplate it replaced forgot one or the other often enough to be worth a rule. Only
// internal/store (which implements WithTx and runs the migrations) may call BeginTx.
func TestTransactionsGoThroughWithTx(t *testing.T) {
	root := repoRoot(t)
	allowed := filepath.Join(root, "internal", "store")
	for _, dir := range sourceDirs(t, root) {
		if dir == allowed {
			continue
		}
		fset, files := parseDir(t, dir)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "BeginTx" || sel.Sel.Name == "Begin") {
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d: %s outside internal/store — use store.WithTx so the transaction is retried when busy and always rolled back",
						filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator))), pos.Line, sel.Sel.Name)
				}
				return true
			})
		}
	}
}

// settingsSQL matches a statement that reads or writes the settings table; sqlVerb keeps
// prose such as "couldn't update settings" in a message from counting as SQL.
var (
	settingsSQL = regexp.MustCompile(`(?i)\b(FROM|INTO|JOIN)\s+settings\b|\bUPDATE\s+settings\s+SET\b`)
	sqlVerb     = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|REPLACE)\b`)
)

// TestSettingsOnlyThroughService: settings are served from memory by settings.Service,
// which writes them through to the table. SQL that reads the table elsewhere can see a
// different value from everyone else, and SQL that writes it changes the database
// without changing what the app runs on. Only internal/settings may touch the table
// (migrations are .sql files, not Go, so they aren't walked).
func TestSettingsOnlyThroughService(t *testing.T) {
	root := repoRoot(t)
	allowed := filepath.Join(root, "internal", "settings")
	for _, dir := range sourceDirs(t, root) {
		if dir == allowed {
			continue
		}
		fset, files := parseDir(t, dir)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !settingsSQL.MatchString(lit.Value) || !sqlVerb.MatchString(lit.Value) {
					return true
				}
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: SQL on the settings table outside internal/settings — use settings.Service (Get/Set/Lookup) so the in-memory copy stays the one truth",
					filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator))), pos.Line)
				return true
			})
		}
	}
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
