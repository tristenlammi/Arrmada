package store_test

import (
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func TestMigrationsApplyFresh(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM convert_failures`).Scan(&n); err != nil {
		t.Fatalf("convert_failures missing after 0059: %v", err)
	}
	var key string
	if err := st.DB().QueryRow(`SELECT item_key FROM convert_failures LIMIT 1`).Scan(&key); err != nil && err.Error() != "sql: no rows in result set" {
		t.Fatalf("item_key column missing: %v", err)
	}
	if _, err := st.DB().Exec(`SELECT path, media_type, video_codec FROM convert_library LIMIT 1`); err != nil {
		t.Fatalf("convert_library missing after 0058: %v", err)
	}
}
