package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// Repositories use the query boundary and WithTx, never a concrete driver pool
// or transaction that can bypass PostgreSQL binding. SQLite test fixtures may
// still use their native pools; parseDir deliberately excludes test files.
func TestRepositoriesUseDatabaseBoundary(t *testing.T) {
	root := repoRoot(t)
	allowed := filepath.Join(root, "internal", "store")
	for _, dir := range sourceDirs(t, root) {
		if dir == allowed {
			continue
		}
		fset, files := parseDir(t, dir)
		for _, f := range files {
			aliases := map[string]bool{}
			for _, imp := range f.Imports {
				if imp.Path.Value == `"database/sql"` {
					name := "sql"
					if imp.Name != nil {
						name = imp.Name.Name
					}
					aliases[name] = true
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if ptr, ok := n.(*ast.StarExpr); ok {
					if sel, ok := ptr.X.(*ast.SelectorExpr); ok && (sel.Sel.Name == "DB" || sel.Sel.Name == "Tx") {
						if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
							pos := fset.Position(ptr.Pos())
							path := filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator)))
							t.Errorf("%s:%d: concrete database pool/transaction outside internal/store — use store.Pool, store.Execer or store.Transaction", path, pos.Line)
						}
					}
				}
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "LastInsertId" {
						pos := fset.Position(call.Pos())
						path := filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator)))
						t.Errorf("%s:%d: LastInsertId is SQLite-specific — use store.InsertID or an explicit RETURNING query", path, pos.Line)
					}
				}
				return true
			})
		}
	}
}
