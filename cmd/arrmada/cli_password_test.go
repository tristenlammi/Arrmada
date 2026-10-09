package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/store"
)

// withAuth opens the test data dir's database for setup or checks, then closes it.
func withAuth(t *testing.T, dir string, fn func(*auth.Service)) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fn(auth.NewService(st.DB()))
}

func TestCLIResetPassword(t *testing.T) {
	dir := testDataDir(t)
	ctx := context.Background()
	var oldSession string
	withAuth(t, dir, func(a *auth.Service) {
		u, err := a.CreateUser(ctx, "owner@example.com", "old-password", auth.RoleAdmin, true)
		if err != nil {
			t.Fatal(err)
		}
		if oldSession, _, err = a.CreateSession(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
	})

	// Matched ignoring case, the way people type their email.
	code, stdout, stderr := runTestCLI(t, "", "reset-password", "Owner@Example.com")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	const prefix = "New password for owner@example.com: "
	var pw string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, prefix) {
			pw = strings.TrimPrefix(line, prefix)
		}
	}
	if len(pw) != 16 || strings.Trim(pw, passwordAlphabet) != "" {
		t.Fatalf("generated password %q isn't 16 characters from the alphabet (output %q)", pw, stdout)
	}

	withAuth(t, dir, func(a *auth.Service) {
		if _, err := a.Authenticate(ctx, "owner@example.com", pw); err != nil {
			t.Errorf("new password doesn't sign in: %v", err)
		}
		if _, err := a.Authenticate(ctx, "owner@example.com", "old-password"); err == nil {
			t.Error("old password still signs in")
		}
		if _, err := a.ValidateSession(ctx, oldSession); err == nil {
			t.Error("the old session survived the reset")
		}
	})
}

// --password-stdin sets the given password and never prints it.
func TestCLIResetPasswordStdin(t *testing.T) {
	dir := testDataDir(t)
	ctx := context.Background()
	withAuth(t, dir, func(a *auth.Service) {
		if _, err := a.CreateUser(ctx, "owner@example.com", "old-password", auth.RoleAdmin, true); err != nil {
			t.Fatal(err)
		}
	})
	const pw = "correct horse battery"
	code, stdout, stderr := runTestCLI(t, pw+"\r\n", "reset-password", "--password-stdin", "owner@example.com")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout+stderr, pw) {
		t.Fatalf("the password was echoed: %q %q", stdout, stderr)
	}
	withAuth(t, dir, func(a *auth.Service) {
		if _, err := a.Authenticate(ctx, "owner@example.com", pw); err != nil {
			t.Errorf("piped password doesn't sign in: %v", err)
		}
	})

	// Too short: refused, and the password stays what it was.
	if code, _, _ := runTestCLI(t, "short\n", "reset-password", "owner@example.com", "--password-stdin"); code != exitFail {
		t.Fatalf("short password: exit %d, want 1", code)
	}
	if code, _, _ := runTestCLI(t, "", "reset-password", "owner@example.com", "--password-stdin"); code != exitFail {
		t.Fatalf("empty stdin: exit %d, want 1", code)
	}
	withAuth(t, dir, func(a *auth.Service) {
		if _, err := a.Authenticate(ctx, "owner@example.com", pw); err != nil {
			t.Errorf("a refused reset changed the password: %v", err)
		}
	})
}

// A name that matches nobody lists the admin usernames, and nothing else.
func TestCLIResetPasswordUnknownEmail(t *testing.T) {
	dir := testDataDir(t)
	ctx := context.Background()
	withAuth(t, dir, func(a *auth.Service) {
		for name, role := range map[string]auth.Role{
			"owner@example.com": auth.RoleAdmin, "partner@example.com": auth.RoleAdmin,
			"manager@example.com": auth.RoleManager, "kid@example.com": auth.RoleRequester,
		} {
			if _, err := a.CreateUser(ctx, name, "password123", role, false); err != nil {
				t.Fatal(err)
			}
		}
	})
	code, stdout, stderr := runTestCLI(t, "", "reset-password", "nobody@example.com")
	if code != exitFail {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "owner@example.com") || !strings.Contains(stderr, "partner@example.com") {
		t.Errorf("admins not listed: %q", stderr)
	}
	if strings.Contains(stderr, "manager@example.com") || strings.Contains(stderr, "kid@example.com") || stdout != "" {
		t.Errorf("listed more than the admins: %q %q", stdout, stderr)
	}

	if code, _, _ := runTestCLI(t, "", "reset-password"); code != exitUsage {
		t.Errorf("no email: exit %d, want 2", code)
	}
}
