package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The maintenance commands for when the web UI can't help — above all when Arrmada won't
// start. They only touch files in the data dir and never open the live database, so they
// are safe while the app is stopped or crash-looping:
//
//	docker compose run --rm --no-deps arrmada-app restore <name>   (then start it)
//	docker exec Arrmada-app arrmada restore <name>                 (then restart it)
//
// The restore itself runs at the next start, exactly like one staged from the Backups card.

// runSubcommand runs a maintenance command named by args[0]. handled is false when args
// isn't one, and the server starts as usual.
func runSubcommand(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "restore", "backups":
	default:
		return 0, false
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "arrmada:", err)
		return 1, true
	}
	if args[0] == "restore" {
		return restoreCommand(cfg.DataDir, args[1:], stdout, stderr), true
	}
	return backupsCommand(cfg.DataDir, args[1:], stdout, stderr), true
}

const restoreUsage = `usage:
  arrmada restore <backup name | path to .db or .db.gz>
      check the backup and put it in place the next time Arrmada starts
  arrmada restore --cancel
      drop a staged restore that hasn't run yet
  arrmada backups
      list the backups in the data dir`

// restoreCommand is `arrmada restore …`.
func restoreCommand(dataDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, restoreUsage)
		return 2
	}
	if args[0] == "--cancel" {
		ok, err := store.CancelRestore(dataDir)
		if err != nil {
			fmt.Fprintln(stderr, "arrmada: couldn't cancel the restore:", err)
			return 1
		}
		if ok {
			fmt.Fprintln(stdout, "Staged restore cancelled. Nothing was changed.")
		} else {
			fmt.Fprintln(stdout, "No restore was staged.")
		}
		return 0
	}
	if strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, restoreUsage)
		return 2
	}

	svc := backup.ForDataDir(dataDir, nil)
	fmt.Fprintln(stdout, "Checking the backup (a large database takes a while)…")
	b, info, err := svc.StageTarget(context.Background(), args[0], "cli")
	if err != nil {
		fmt.Fprintln(stderr, "arrmada: nothing was staged:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Staged %s (%s, taken %s, schema %s).\n", b.Name, b.Kind, b.CreatedAt.Local().Format(time.DateTime), info.SchemaVersion)
	fmt.Fprintln(stdout, "It replaces the database the next time Arrmada starts; the current one is kept as a pre-restore backup.")
	fmt.Fprintln(stdout, "Everything since the backup was taken will be lost. Start or restart Arrmada now, or undo this with: arrmada restore --cancel")
	return 0
}

// backupsCommand is `arrmada backups`: what's in the backups folder, newest first.
func backupsCommand(dataDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, restoreUsage)
		return 2
	}
	svc := backup.ForDataDir(dataDir, nil)
	list, err := svc.List(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "arrmada:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No backups in", svc.Dir())
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tTAKEN\tSIZE\tSCHEMA\tNAME")
		for _, b := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", b.Kind, b.CreatedAt.Local().Format(time.DateTime), humanBytes(b.SizeBytes), b.SchemaVersion, b.Name)
		}
		_ = tw.Flush()
	}
	if m, err := svc.PendingRestore(); err == nil && m != nil {
		fmt.Fprintf(stdout, "\nA restore of %s is staged for the next start (arrmada restore --cancel to drop it).\n", m.Name())
	}
	return 0
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
