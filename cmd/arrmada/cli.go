package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/store"
)

// `arrmada <command> [args]` runs one maintenance command and exits; with no arguments
// the binary is the server. Scripts reach these with `docker exec Arrmada-app arrmada …`
// (update.sh's pre-update backup) or `docker compose run --rm --no-deps arrmada-app …`
// when the app won't start. Before this, every argument was ignored, so a `docker exec`
// with arguments quietly started a second full server inside the container.

// cliCommand is one subcommand. Adding a command is one entry in cliCommands.
type cliCommand struct {
	name    string
	args    string // what follows the name in the usage line
	summary string
	// dataDir marks commands that open files in the data folder. Run as root (a plain
	// docker exec), they first switch to the user that owns the database, so nothing
	// they create is root's and unwritable by the app.
	dataDir bool
	run     func(ctx context.Context, c *cli, args []string) int
}

var cliCommands = []cliCommand{
	{name: "version", args: "[--schema]", summary: "print the version and commit this binary was built from", run: cmdVersion},
	{name: "schema", summary: "compare the database's schema with this build's (exit 3: a newer build upgraded it)", dataDir: true, run: cmdSchema},
	{name: "reset-password", args: "<email> [--password-stdin]", summary: "give a locked-out account a new password and sign it out everywhere", dataDir: true, run: cmdResetPassword},
	{name: "backup", args: "[--kind manual|pre-update]", summary: "copy the database to <data>/backups while the app keeps running", dataDir: true, run: cmdBackup},
}

// cli is what a command runs with: its streams and, for dataDir commands, the config.
type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	cfg            config.Config
}

// Exit codes: 0 done, 1 failed, 2 used wrongly.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// runCLI runs a command from the process's arguments (without the program name).
func runCLI(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLIWith(ctx, &cli{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, args)
}

func runCLIWith(ctx context.Context, c *cli, args []string) int {
	if len(args) == 0 {
		cliUsage(c.stderr)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		cliUsage(c.stdout)
		return exitOK
	}
	var cmd *cliCommand
	for i := range cliCommands {
		if cliCommands[i].name == args[0] {
			cmd = &cliCommands[i]
			break
		}
	}
	if cmd == nil {
		fmt.Fprintf(c.stderr, "arrmada: unknown command %q\n\n", args[0])
		cliUsage(c.stderr)
		return exitUsage
	}
	if cmd.dataDir {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(c.stderr, "arrmada %s: %v\n", cmd.name, err)
			return exitFail
		}
		c.cfg = cfg
		// Before anything is opened: see becomeDataOwner.
		if err := becomeDataOwner(cfg.DataDir); err != nil {
			fmt.Fprintf(c.stderr, "arrmada %s: %v\n", cmd.name, err)
			return exitFail
		}
	}
	return cmd.run(ctx, c, args[1:])
}

func cliUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: arrmada                 run the server")
	fmt.Fprintln(w, "       arrmada <command> ...   run one maintenance command and exit")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, cmd := range cliCommands {
		fmt.Fprintf(w, "  %-16s %s\n", cmd.name, cmd.summary)
		if cmd.args != "" {
			fmt.Fprintf(w, "  %-16s   arrmada %s %s\n", "", cmd.name, cmd.args)
		}
	}
}

// newFlags is a flag set for one command that reports problems instead of exiting.
func newFlags(c *cli, name string) *flag.FlagSet {
	fs := flag.NewFlagSet("arrmada "+name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	return fs
}

// parseFlags parses args, allowing flags after positional arguments too
// ("reset-password you@example.com --password-stdin"), and returns the positionals.
// When stop is true the command ends with code: -h was asked for, or the flags were
// wrong (the flag package has already printed why).
func parseFlags(fs *flag.FlagSet, args []string) (pos []string, stop bool, code int) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, true, exitOK
			}
			return nil, true, exitUsage
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, false, exitOK
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// cmdVersion prints what this binary was built from. It opens nothing, so it is safe
// to run against any image.
func cmdVersion(_ context.Context, c *cli, args []string) int {
	fs := newFlags(c, "version")
	schemaOnly := fs.Bool("schema", false, "print only the newest database migration this build has")
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(c.stderr, "arrmada version: unexpected argument %q\n", pos[0])
		return exitUsage
	}
	if *schemaOnly {
		fmt.Fprintln(c.stdout, store.LatestMigration())
		return exitOK
	}
	fmt.Fprintf(c.stdout, "Arrmada %s (commit %s, %s, schema %s)\n",
		buildinfo.Version, buildinfo.Commit, runtime.Version(), store.LatestMigration())
	return exitOK
}

// exitNewerSchema is cmdSchema's answer when a newer build has upgraded the database:
// this binary would refuse to start on it.
const exitNewerSchema = 3

// cmdSchema compares the database's schema with this build's without changing it.
// update.sh asks the build it's about to roll back to, before stopping anything.
func cmdSchema(ctx context.Context, c *cli, args []string) int {
	fs := newFlags(c, "schema")
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(c.stderr, "arrmada schema: unexpected argument %q\n", pos[0])
		return exitUsage
	}
	st, err := store.OpenNoMigrate(c.cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada schema: %v\n", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	s, err := st.Schema(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada schema: %v\n", err)
		return exitFail
	}
	applied := s.Applied
	if applied == "" {
		applied = "none"
	}
	fmt.Fprintf(c.stdout, "database:   %s\nthis build: %s\n", applied, s.Latest)
	switch {
	case len(s.Unknown) > 0:
		fmt.Fprintf(c.stdout, "A newer Arrmada has upgraded this database (%s); this build won't start on it.\n",
			strings.Join(s.Unknown, ", "))
		return exitNewerSchema
	case len(s.Pending) > 0:
		fmt.Fprintf(c.stdout, "This build will upgrade the database when it starts (%d migrations).\n", len(s.Pending))
	default:
		fmt.Fprintln(c.stdout, "The database is up to date for this build.")
	}
	return exitOK
}

// cmdBackup takes a checked copy of the live database (the same VACUUM INTO snapshot
// the app's own backups use) without stopping the server, prunes that kind to its
// retention, and prints the new file's path.
func cmdBackup(ctx context.Context, c *cli, args []string) int {
	fs := newFlags(c, "backup")
	kind := fs.String("kind", string(store.BackupManual), "manual or pre-update")
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(c.stderr, "arrmada backup: unexpected argument %q\n", pos[0])
		return exitUsage
	}
	k := store.BackupKind(*kind)
	if k != store.BackupManual && k != store.BackupPreUpdate {
		fmt.Fprintf(c.stderr, "arrmada backup: --kind must be manual or pre-update, not %q\n", *kind)
		return exitUsage
	}

	// Never migrates: a backup must copy the database as it is, and the server running
	// beside this owns upgrades.
	st, err := store.OpenNoMigrate(c.cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada backup: %v\n", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()

	log := slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := backup.New(st, nil, log)
	b, err := svc.Create(ctx, k)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada backup: %v\n", err)
		return exitFail
	}
	fmt.Fprintln(c.stdout, filepath.Join(svc.Dir(), b.Name))
	return exitOK
}
