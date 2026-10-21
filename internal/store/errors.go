package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation classifies constraint failures without relying on driver
// messages, locales, or the name of a particular table or index. Both primary
// keys and unique indexes count; foreign-key, check and not-null failures do not.
func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505"
	}
	var sq *sqlite.Error
	return errors.As(err, &sq) && (sq.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sq.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
}
