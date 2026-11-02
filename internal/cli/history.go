package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

func statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what has changed since the newest snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := openEnv()
			if err != nil {
				return err
			}
			cfg, err := e.loadConfig()
			if err != nil {
				return err
			}
			s, err := snapshot.Find(e.backupDir, "")
			if err != nil {
				return err
			}
			idx, _, err := openIndex(s)
			if err != nil {
				return err
			}
			// A dry run collects exactly what a backup would. Comparing like with
			// like means using the secrets-included set for an encrypted snapshot.
			res, err := backup.Run(backup.Options{
				Home: e.home, Dir: e.backupDir, Config: cfg, Encrypt: s.Encrypted, DryRun: true,
			})
			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			if err != nil {
				return err
			}
			changes := visibleChanges(archive.Diff(idx.Entries, res.Entries))
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Snapshot  string           `json:"snapshot"`
					Created   time.Time        `json:"created"`
					Encrypted bool             `json:"encrypted"`
					Changes   []archive.Change `json:"changes"`
				}{s.Name, s.Time, s.Encrypted, changes})
			}
			if len(changes) == 0 {
				fmt.Printf("no changes since %s (%s)\n", s.Name, humanAge(time.Since(s.Time)))
				return nil
			}
			fmt.Printf("changes since %s (%s):\n\n", s.Name, humanAge(time.Since(s.Time)))
			printChanges(changes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func diffCmd() *cobra.Command {
	var asJSON bool
	var patterns []string
	cmd := &cobra.Command{
		Use:   "diff [old] [new]",
		Short: "List what changed between two snapshots",
		Long:  "List what changed between two snapshots. With no arguments, compares the previous snapshot with the latest. With one, compares that snapshot with the latest.",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := openEnv()
			if err != nil {
				return err
			}
			older, newer, err := diffTargets(e.backupDir, args)
			if err != nil {
				return err
			}
			var u unlocker
			oldIdx, err := u.index(older)
			if err != nil {
				return err
			}
			newIdx, err := u.index(newer)
			if err != nil {
				return err
			}
			if older.Encrypted != newer.Encrypted {
				fmt.Fprintln(os.Stderr, "note: only one of these is encrypted, so secrets will show as added or removed")
			}
			changes := visibleChanges(archive.Diff(oldIdx.Entries, newIdx.Entries))
			if len(patterns) > 0 {
				keep := restore.Filter(patterns, e.home)
				kept := make([]archive.Change, 0, len(changes))
				for _, c := range changes {
					if keep(archive.Entry{Path: c.Path}) {
						kept = append(kept, c)
					}
				}
				changes = kept
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Old     string           `json:"old"`
					New     string           `json:"new"`
					Changes []archive.Change `json:"changes"`
				}{older.Name, newer.Name, changes})
			}
			if len(changes) == 0 {
				fmt.Printf("no changes from %s to %s\n", older.Name, newer.Name)
				return nil
			}
			fmt.Printf("changes from %s to %s:\n\n", older.Name, newer.Name)
			printChanges(changes)
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&patterns, "path", "p", nil, "only show these paths or globs (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

// diffTargets picks the two snapshots `diff` compares.
func diffTargets(dir string, args []string) (older, newer snapshot.Snapshot, err error) {
	switch len(args) {
	case 2:
		if older, err = snapshot.Find(dir, args[0]); err != nil {
			return
		}
		newer, err = snapshot.Find(dir, args[1])
		return
	case 1:
		if older, err = snapshot.Find(dir, args[0]); err != nil {
			return
		}
		newer, err = snapshot.Find(dir, "")
		return
	}
	snaps, err := snapshot.List(dir)
	if err != nil {
		return
	}
	if len(snaps) < 2 {
		return older, newer, fmt.Errorf("need two snapshots to compare, and %s has %d", dir, len(snaps))
	}
	return snaps[1], snaps[0], nil
}

func logCmd() *cobra.Command {
	var asJSON, noEncrypted bool
	cmd := &cobra.Command{
		Use:   "log <path or glob>...",
		Short: "Show which snapshots changed a file",
		Long: "Show which snapshots changed a file, newest first. A file's first appearance is the oldest " +
			"snapshot still kept that has it, so `added` can be later than when it was really created.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := openEnv()
			if err != nil {
				return err
			}
			snaps, err := snapshot.List(e.backupDir)
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				return fmt.Errorf("%w in %s", snapshot.ErrNone, e.backupDir)
			}
			// One prompt for the whole walk: asking again for each of ten
			// snapshots is worse than marking the ones it can't open.
			var u unlocker
			u.once = true
			u.skip = noEncrypted
			loaded := make([]loadedSnapshot, 0, len(snaps))
			skipped := 0
			for _, s := range snaps {
				idx, err := u.index(s)
				switch {
				case errors.Is(err, errSkipEncrypted):
					skipped++
					continue
				case errors.Is(err, archive.ErrBadPassphrase):
					loaded = append(loaded, loadedSnapshot{snap: s, unreadable: kindLocked})
				case err != nil:
					fmt.Fprintf(os.Stderr, "warning: %s: %v\n", s.Name, err)
					loaded = append(loaded, loadedSnapshot{snap: s, unreadable: kindUnreadable})
				default:
					loaded = append(loaded, loadedSnapshot{snap: s, entries: idx.Entries})
				}
			}
			if skipped > 0 && !noEncrypted {
				fmt.Fprintf(os.Stderr, "note: skipped %d encrypted snapshot(s): %v\n", skipped, u.promptErr)
			}
			rows := fileHistory(loaded, restore.Filter(args, e.home))
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Printf("no changes in %d snapshot(s)\n", len(loaded))
				return nil
			}
			hidden := false
			for _, r := range rows {
				fmt.Printf("%s  %-8s  %s\n", r.Snapshot, r.Kind, r.Path)
				hidden = hidden || r.Kind == kindLocked || r.Kind == kindUnreadable
			}
			if hidden {
				fmt.Fprintln(os.Stderr, "note: changes in a snapshot that couldn't be read show up at the next one that could")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noEncrypted, "no-encrypted", false, "skip encrypted snapshots without asking for a passphrase")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

const (
	kindLocked     archive.ChangeKind = "locked"
	kindUnreadable archive.ChangeKind = "unreadable"
)

type loadedSnapshot struct {
	snap    snapshot.Snapshot
	entries []archive.Entry
	// unreadable is set instead of entries when the index couldn't be read.
	unreadable archive.ChangeKind
}

type logRow struct {
	Snapshot string             `json:"snapshot"`
	Kind     archive.ChangeKind `json:"kind"`
	Path     string             `json:"path,omitempty"`
}

// fileHistory walks snapshots, given newest first, and returns the changes to
// matching files, newest first.
//
// Plain snapshots leave secrets out, so a secret missing from a plain snapshot
// hasn't been deleted. Comparing neighbors would report it removed and added
// again each time the two kinds alternate. So a file last seen in an encrypted
// snapshot only counts as removed once an encrypted snapshot lacks it too.
func fileHistory(snaps []loadedSnapshot, keep func(archive.Entry) bool) []logRow {
	type seen struct {
		entry     archive.Entry
		encrypted bool
	}
	state := map[string]seen{}
	var rows []logRow
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if s.unreadable != "" {
			rows = append(rows, logRow{Snapshot: s.snap.Name, Kind: s.unreadable})
			continue
		}
		old := make([]archive.Entry, 0, len(state))
		for _, st := range state {
			old = append(old, st.entry)
		}
		var cur []archive.Entry
		for _, e := range s.entries {
			if e.Type != archive.TypeDir && keep(e) {
				cur = append(cur, e)
			}
		}
		for _, c := range archive.Diff(old, cur) {
			if c.Kind == archive.Removed {
				if !s.snap.Encrypted && state[c.Path].encrypted {
					continue
				}
				delete(state, c.Path)
			}
			rows = append(rows, logRow{Snapshot: s.snap.Name, Kind: c.Kind, Path: c.Path})
		}
		for _, e := range cur {
			state[e.Path] = seen{e, s.snap.Encrypted}
		}
	}
	// Rows were built oldest first. Show the newest snapshot first, keeping each
	// snapshot's own rows in path order.
	order := make(map[string]int, len(snaps))
	for i, s := range snaps {
		order[s.snap.Name] = i
	}
	sort.SliceStable(rows, func(i, j int) bool { return order[rows[i].Snapshot] < order[rows[j].Snapshot] })
	if rows == nil {
		rows = []logRow{}
	}
	return rows
}

// visibleChanges hides folders that were added or removed along with their
// contents, since the files are already listed. An empty folder stays, or a
// change that causes a new snapshot wouldn't appear anywhere. The result is
// never nil, so it encodes as an empty JSON list.
func visibleChanges(changes []archive.Change) []archive.Change {
	parents := map[string]bool{}
	for _, c := range changes {
		for dir := path.Dir(c.Path); dir != "."; dir = path.Dir(dir) {
			parents[dir] = true
		}
	}
	out := make([]archive.Change, 0, len(changes))
	for _, c := range changes {
		if c.Kind == archive.Added && c.New.Type == archive.TypeDir && parents[c.Path] {
			continue
		}
		if c.Kind == archive.Removed && c.Old.Type == archive.TypeDir && parents[c.Path] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func printChanges(changes []archive.Change) {
	for _, c := range changes {
		fmt.Printf("%-8s  %s\n", c.Kind, c.Path)
	}
}

func humanAge(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", unit)
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}

var errSkipEncrypted = errors.New("skipping encrypted snapshot")

// unlocker reads snapshot indexes, reusing a passphrase once it's been entered.
type unlocker struct {
	pass string
	// once means ask at most one time, and report a wrong passphrase rather
	// than asking again.
	once bool
	// skip means never ask, and skip encrypted snapshots.
	skip      bool
	asked     bool
	promptErr error
}

func (u *unlocker) index(s snapshot.Snapshot) (archive.Index, error) {
	if !s.Encrypted {
		return archive.ReadIndex(s.Path, "")
	}
	if u.skip {
		return archive.Index{}, errSkipEncrypted
	}
	if u.pass != "" {
		idx, err := archive.ReadIndex(s.Path, u.pass)
		if !errors.Is(err, archive.ErrBadPassphrase) || u.once {
			return idx, err
		}
	}
	if u.once && u.asked {
		return archive.Index{}, errSkipEncrypted
	}
	u.asked = true
	p, err := passphrase("Passphrase for "+s.Name+": ", false)
	if err != nil {
		if !u.once {
			return archive.Index{}, err
		}
		// With no terminal to ask on, log still works for the plain snapshots.
		u.promptErr = err
		return archive.Index{}, errSkipEncrypted
	}
	u.pass = p
	return archive.ReadIndex(s.Path, p)
}
