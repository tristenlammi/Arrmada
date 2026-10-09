package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/store"
)

// cmdResetPassword gives an account a new password from the server's shell, for an
// owner locked out of the only admin account (there is no emailed reset). Anyone who
// can run commands in the container can do this; for a self-hosted app that is the
// same person who could edit the database by hand.
//
// The password is printed to this terminal once and goes nowhere else: no log line,
// no file. Setting it signs the account out of every session.
func cmdResetPassword(ctx context.Context, c *cli, args []string) int {
	fs := newFlags(c, "reset-password")
	fromStdin := fs.Bool("password-stdin", false, "read the new password from standard input instead of generating one")
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(c.stderr, "Usage: arrmada reset-password <email> [--password-stdin]")
		return exitUsage
	}
	login := strings.TrimSpace(pos[0])

	st, err := store.OpenNoMigrate(c.cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	// Writing to a schema this build doesn't know is what the server refuses to do too.
	if s, err := st.Schema(ctx); err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
		return exitFail
	} else if len(s.Unknown) > 0 && !c.cfg.AllowNewerSchema {
		fmt.Fprintln(c.stderr, "arrmada reset-password: a newer Arrmada has upgraded this database; run reset-password from that build (update Arrmada first).")
		return exitFail
	}

	svc := auth.NewService(st.DB())
	u, err := svc.UserByUsername(ctx, login)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		fmt.Fprintf(c.stderr, "arrmada reset-password: no account is called %q.\n", login)
		listAdmins(ctx, c.stderr, svc)
		return exitFail
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
		return exitFail
	}

	var password string
	if *fromStdin {
		password, err = readPassword(c.stdin)
		if err != nil {
			fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
			return exitFail
		}
	} else if password, err = generatePassword(16); err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
		return exitFail
	}

	if err := svc.SetPassword(ctx, u.ID, password); err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			fmt.Fprintln(c.stderr, "arrmada reset-password: the password must be at least 8 characters; nothing was changed.")
		} else {
			fmt.Fprintf(c.stderr, "arrmada reset-password: %v\n", err)
		}
		return exitFail
	}

	if *fromStdin {
		fmt.Fprintf(c.stdout, "The password for %s has been changed.\n", u.Username)
	} else {
		fmt.Fprintf(c.stdout, "New password for %s: %s\n", u.Username, password)
		fmt.Fprintln(c.stdout, "It's shown only here, once. Sign in with it, then change it.")
	}
	fmt.Fprintln(c.stdout, "Every session this account had has been signed out.")
	if u.Disabled {
		fmt.Fprintln(c.stdout, "This account is disabled, so it can't sign in until an admin enables it again.")
	}
	return exitOK
}

// listAdmins names the admin accounts (names only) so a mistyped email can be fixed.
func listAdmins(ctx context.Context, w io.Writer, svc *auth.Service) {
	users, err := svc.ListUsers(ctx)
	if err != nil {
		return
	}
	var admins []string
	for _, u := range users {
		if u.Role == auth.RoleAdmin {
			admins = append(admins, u.Username)
		}
	}
	if len(admins) == 0 {
		fmt.Fprintln(w, "There are no admin accounts yet: open Arrmada in a browser to create one.")
		return
	}
	fmt.Fprintf(w, "Admin accounts: %s\n", strings.Join(admins, ", "))
}

// readPassword reads one line from in: a password piped or typed with
// --password-stdin, so it never appears in the command line or the shell history.
func readPassword(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password on standard input; nothing was changed")
	}
	return line, nil
}

// passwordAlphabet leaves out characters that are easy to misread from a terminal
// (0/O/o, 1/l/I/i).
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatePassword draws n characters uniformly from passwordAlphabet with crypto/rand.
func generatePassword(n int) (string, error) {
	size := big.NewInt(int64(len(passwordAlphabet)))
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, size)
		if err != nil {
			return "", fmt.Errorf("generate a password: %w", err)
		}
		b[i] = passwordAlphabet[k.Int64()]
	}
	return string(b), nil
}
