package archtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellFunction(t *testing.T, source, name string) string {
	t.Helper()
	start := strings.Index(source, name+"() {\n")
	if start < 0 {
		t.Fatalf("missing shell function %s", name)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("unterminated shell function %s", name)
	}
	return source[start:start+end+3] + "\n"
}

func TestPostgresUpdateGuardsBeforeDestructiveActions(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	functions := shellFunction(t, string(raw), "database_backend") + shellFunction(t, string(raw), "require_database_backend_image") + shellFunction(t, string(raw), "rollback")
	for _, test := range []struct {
		name, backend, withDB, backup, label, command string
		success                                       bool
	}{
		{"postgres-db-rollback", "postgres", "1", "arrmada-pre-update.pgdump", "1", "rollback", false},
		{"native-copy-after-backend-switch", "sqlite", "1", "arrmada-pre-update.pgdump", "", "rollback", false},
		{"old-image-rollback", "Postgres", "", "", "", "rollback", false},
		{"old-pulled-image", "postgres", "", "", "", "require_database_backend_image candidate", false},
		{"postgres-capable-image", "postgres", "", "", "1", "require_database_backend_image candidate", true},
		{"sqlite-legacy-image", "sqlite", "", "", "", "require_database_backend_image candidate", true},
		{"invalid-backend", "posgres", "", "", "", "require_database_backend_image candidate", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			backup := filepath.Join(dir, "previous-db")
			if err := os.WriteFile(backup, []byte(test.backup), 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "destructive-action")
			harness := `say() { printf '%s\n' "$*"; }
env_get() { :; }
image_id() { if [ "$1" = arrmada:previous ]; then printf previous; else printf current; fi; }
image_label() { printf '%s' "$TEST_LABEL"; }
docker() { if [ "$*" = 'compose config --format json' ]; then printf '{"ARRMADA_DATABASE_BACKEND":"%s"}\n' "$ARRMADA_DATABASE_BACKEND"; return; fi; touch "$TEST_MARKER"; exit 90; }
confirm() { touch "$TEST_MARKER"; exit 91; }
` + functions + "\n" + test.command + "\n"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", harness)
			cmd.Env = append(os.Environ(), "ARRMADA_DATABASE_BACKEND="+test.backend, "WITH_DB="+test.withDB, "PREVIOUS_DB_FILE="+backup, "TEST_LABEL="+test.label, "TEST_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.success {
				t.Fatalf("guard outcome: %v, %s", err, output)
			}
			if !test.success && !strings.Contains(string(output), "nothing was changed") && !strings.Contains(string(output), "no containers, images or schemas were changed") {
				t.Fatalf("guard did not explain recovery: %s", output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("guard performed a destructive action")
			}
		})
	}
}
