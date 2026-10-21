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
	"github.com/tristenlammi/arrmada/internal/settings"
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

	st, err := openMaintenanceStore(ctx, c.cfg)
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
	if on, err := svc.TwoFactorEnabled(ctx, u.ID); err == nil && on {
		fmt.Fprintf(c.stdout, "Two-factor sign-in is still on: signing in asks for a code too. If the authenticator and recovery codes are lost as well, run: arrmada reset-2fa %s\n", u.Username)
	}
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

// cmdResetTwoFactor turns off two-factor sign-in for an account from the server's shell,
// for an owner whose phone and recovery codes are both gone (an admin can do it for anyone
// else in People & access). Like reset-password, whoever can run commands in the container
// could edit the database by hand anyway.
//
// The password stays as it is and so do the account's sessions: the password still opens
// it, now without a code. Its recovery codes and trusted devices go with the secret.
//
// --policy-off also switches off "Require two-factor for admins", with or without an
// account: with it on, an admin whose two-factor was just turned off is asked to set it up
// again at the next sign-in, which an owner without an authenticator app can't do. The
// running app reads settings at start, so that change needs a restart.
func cmdResetTwoFactor(ctx context.Context, c *cli, args []string) int {
	fs := newFlags(c, "reset-2fa")
	policyOff := fs.Bool("policy-off", false, `also switch off "Require two-factor for admins" (takes effect when Arrmada restarts)`)
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) > 1 || (len(pos) == 0 && !*policyOff) {
		fmt.Fprintln(c.stderr, "Usage: arrmada reset-2fa <email> [--policy-off]")
		fmt.Fprintln(c.stderr, "       arrmada reset-2fa --policy-off")
		return exitUsage
	}

	st, err := openMaintenanceStore(ctx, c.cfg)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	if s, err := st.Schema(ctx); err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
		return exitFail
	} else if len(s.Unknown) > 0 && !c.cfg.AllowNewerSchema {
		fmt.Fprintln(c.stderr, "arrmada reset-2fa: a newer Arrmada has upgraded this database; run reset-2fa from that build (update Arrmada first).")
		return exitFail
	}
	set, err := settings.Open(ctx, st.DB())
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
		return exitFail
	}
	svc := auth.NewService(st.DB())

	var u *auth.User
	if len(pos) == 1 {
		login := strings.TrimSpace(pos[0])
		u, err = svc.UserByUsername(ctx, login)
		if errors.Is(err, auth.ErrInvalidCredentials) {
			fmt.Fprintf(c.stderr, "arrmada reset-2fa: no account is called %q.\n", login)
			listAdmins(ctx, c.stderr, svc)
			return exitFail
		}
		if err != nil {
			fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
			return exitFail
		}
	}

	policyWasOn := set.GetBool(ctx, settings.KeyTwoFactorAdmins, false)
	if *policyOff {
		if err := set.SetBool(ctx, settings.KeyTwoFactorAdmins, false); err != nil {
			fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
			return exitFail
		}
		if policyWasOn {
			fmt.Fprintln(c.stdout, `"Require two-factor for admins" is now off. Restart Arrmada for the running app to see it (docker restart Arrmada-app).`)
		} else {
			fmt.Fprintln(c.stdout, `"Require two-factor for admins" was already off.`)
		}
	}
	if u == nil {
		return exitOK
	}

	was, err := svc.DisableTwoFactor(ctx, u.ID)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada reset-2fa: %v\n", err)
		return exitFail
	}
	if !was {
		fmt.Fprintf(c.stdout, "Two-factor sign-in wasn't on for %s; nothing to turn off.\n", u.Username)
	} else if policyWasOn && !*policyOff && u.Role == auth.RoleAdmin {
		// The admin policy would ask for a new setup at once: say so, and the way out.
		fmt.Fprintf(c.stdout, "Two-factor sign-in is off for %s. This server requires it of admins, so signing in with the password will ask you to set it up again straight away, with an authenticator app.\n", u.Username)
		fmt.Fprintln(c.stdout, "If you can't, switch that requirement off too: arrmada reset-2fa --policy-off (then restart Arrmada).")
	} else {
		fmt.Fprintf(c.stdout, "Two-factor sign-in is off for %s. Sign in with the password, then set it up again from your Me page.\n", u.Username)
	}
	if u.Disabled {
		fmt.Fprintln(c.stdout, "This account is disabled, so it can't sign in until an admin enables it again.")
	}
	return exitOK
}
