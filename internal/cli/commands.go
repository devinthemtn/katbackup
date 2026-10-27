package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/scan"
	"github.com/devinthemtn/katbackup/internal/snapshot"
	"github.com/devinthemtn/katbackup/internal/tui"
)

func initCmd() *cobra.Command {
	var force, useTUI bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scan your home folder and write a manifest of config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			if _, err := os.Stat(e.cfgPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", e.cfgPath)
			}
			cands, err := scan.Scan(e.home)
			if err != nil {
				return err
			}
			cfg := config.Default()
			if useTUI {
				inc, sec, ok, err := tui.RunInit(cands)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Println("cancelled")
					return nil
				}
				cfg.Include, cfg.Secrets = inc, sec
			} else {
				for _, c := range cands {
					switch {
					case c.Suggested:
						cfg.Include = append(cfg.Include, "~/"+c.Path)
					case c.Has(scan.FlagSecret) && !c.Has(scan.FlagNoise):
						cfg.Secrets = append(cfg.Secrets, "~/"+c.Path)
					}
				}
			}
			cfg.Secrets = append(cfg.Secrets, prefixed(scan.SecretsInside(unprefixed(cfg.Include)))...)
			if err := config.Save(e.cfgPath, cfg); err != nil {
				return err
			}
			fmt.Printf("wrote %s (%d includes, %d secrets)\n", e.cfgPath, len(cfg.Include), len(cfg.Secrets))
			fmt.Println("edit it to taste, then run `katbackup backup`")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing config")
	cmd.Flags().BoolVar(&useTUI, "tui", false, "pick paths interactively")
	return cmd
}

func prefixed(rel []string) []string {
	out := make([]string, len(rel))
	for i, r := range rel {
		out[i] = "~/" + r
	}
	return out
}

func unprefixed(p []string) []string {
	out := make([]string, len(p))
	for i, s := range p {
		out[i] = strings.TrimPrefix(s, "~/")
	}
	return out
}

func scanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "List candidate config paths in your home folder",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			cands, err := scan.Scan(e.home)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SUGGEST\tPATH\tSIZE\tFLAGS")
			for _, c := range cands {
				mark, size := " ", "-"
				if c.Suggested {
					mark = "✓"
				}
				if c.Size >= 0 {
					size = human(c.Size)
				}
				flags := make([]string, len(c.Flags))
				for i, f := range c.Flags {
					flags[i] = string(f)
				}
				name := "~/" + c.Path
				if c.IsDir {
					name += "/"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, name, size, strings.Join(flags, ","))
			}
			return tw.Flush()
		},
	}
}

func backupCmd() *cobra.Command {
	var encrypt, noEncrypt, dryRun, keepDir bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Copy configured files into ~/.katConfigBackup and compress them",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			cfg, err := e.loadConfig()
			if err != nil {
				return err
			}
			var pass string
			if (cfg.Encrypt || encrypt) && !noEncrypt && !dryRun {
				if pass, err = passphrase("New snapshot passphrase: ", true); err != nil {
					return err
				}
			}
			res, err := backup.Run(backup.Options{
				Home: e.home, Config: cfg, Passphrase: pass, DryRun: dryRun, KeepDir: keepDir,
			})
			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			if err != nil {
				return err
			}
			if dryRun {
				for _, en := range res.Entries {
					if en.Type != archive.TypeDir {
						fmt.Println(en.Path)
					}
				}
				fmt.Printf("\n%d entries, %s (dry run, nothing written)\n", len(res.Entries), human(res.Bytes))
				return nil
			}
			info, _ := os.Stat(res.Archive)
			var size int64
			if info != nil {
				size = info.Size()
			}
			fmt.Printf("backed up %d entries (%s) → %s (%s)\n", len(res.Entries), human(res.Bytes), res.Archive, human(size))
			if res.Staging != "" {
				fmt.Println("staging copy kept at", res.Staging)
			}
			for _, p := range res.Pruned {
				fmt.Println("pruned", p.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&encrypt, "encrypt", "e", false, "encrypt with a passphrase and include secrets")
	cmd.Flags().BoolVar(&noEncrypt, "no-encrypt", false, "don't encrypt even if the config says to")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "list what would be backed up")
	cmd.Flags().BoolVar(&keepDir, "keep-dir", false, "keep the uncompressed staging copy")
	return cmd
}

func listCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			snaps, err := snapshot.List(e.backupDir)
			if err != nil {
				return err
			}
			type row struct {
				Name      string `json:"name"`
				Path      string `json:"path"`
				Size      int64  `json:"size"`
				Files     int    `json:"files"` // -1 when encrypted
				Encrypted bool   `json:"encrypted"`
			}
			rows := make([]row, 0, len(snaps))
			for _, s := range snaps {
				r := row{Name: s.Name, Path: s.Path, Size: s.Size, Encrypted: s.Encrypted, Files: -1}
				if !s.Encrypted {
					if idx, err := archive.ReadIndex(s.Path, ""); err == nil {
						r.Files = countFiles(idx)
					}
				}
				rows = append(rows, r)
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Println("no snapshots yet; run `katbackup backup`")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SNAPSHOT\tSIZE\tFILES\tENCRYPTED")
			for _, r := range rows {
				files, enc := "?", ""
				if r.Files >= 0 {
					files = fmt.Sprint(r.Files)
				}
				if r.Encrypted {
					enc = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, human(r.Size), files, enc)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func countFiles(idx archive.Index) int {
	n := 0
	for _, e := range idx.Entries {
		if e.Type != archive.TypeDir {
			n++
		}
	}
	return n
}

func showCmd() *cobra.Command {
	var tree, asJSON bool
	cmd := &cobra.Command{
		Use:   "show [snapshot]",
		Short: "List files in a snapshot (latest by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			s, err := snapshot.Find(e.backupDir, first(args))
			if err != nil {
				return err
			}
			idx, _, err := openIndex(s)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(idx)
			}
			fmt.Printf("%s  host=%s  created=%s\n\n", s.Name, idx.Hostname, idx.Created.Format("2006-01-02 15:04:05"))
			for _, en := range idx.Entries {
				name := en.Path
				if tree {
					depth := strings.Count(en.Path, "/")
					name = strings.Repeat("  ", depth) + en.Path[strings.LastIndex(en.Path, "/")+1:]
				}
				switch en.Type {
				case archive.TypeDir:
					if !tree {
						continue
					}
					fmt.Println(name + "/")
				case archive.TypeSymlink:
					fmt.Printf("%s -> %s\n", name, en.LinkTarget)
				default:
					fmt.Printf("%s  (%s)\n", name, human(en.Size))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&tree, "tree", "t", false, "show as an indented tree")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the raw index as JSON")
	return cmd
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func restoreCmd() *cobra.Command {
	var snap, to string
	var dryRun, force, yes bool
	cmd := &cobra.Command{
		Use:   "restore [paths or globs...]",
		Short: "Restore all files, or only matching ones, from a snapshot",
		Example: `  katbackup restore                       # everything from the latest snapshot
  katbackup restore ~/.zshrc .config/nvim  # just these
  katbackup restore -s 2026-10-26 '.config/**/*.toml' --dry-run
  katbackup restore --to /tmp/out          # extract somewhere else`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			s, err := snapshot.Find(e.backupDir, snap)
			if err != nil {
				return err
			}
			idx, pass, err := openIndex(s)
			if err != nil {
				return err
			}
			root := e.home
			if to != "" {
				root = to
			}
			actions := restore.Plan(idx, restore.Filter(args, e.home), root)
			if len(actions) == 0 {
				return errors.New("no entries in the snapshot match")
			}
			pending := restore.Pending(actions)
			for _, a := range pending {
				if a.Entry.Type == archive.TypeDir && a.Kind == restore.Create {
					continue
				}
				fmt.Printf("%-9s %s\n", a.Kind, a.Target)
			}
			fmt.Printf("\n%d to write, %d unchanged\n", len(pending), len(actions)-len(pending))
			if dryRun || len(pending) == 0 {
				return nil
			}
			if !yes && !confirm(fmt.Sprintf("Restore from %s?", s.Name)) {
				return errors.New("aborted (pass --yes to skip the prompt)")
			}
			moved, err := restore.Apply(s.Path, pass, actions, restore.Options{Force: force})
			for _, m := range moved {
				fmt.Println("kept previous version at", m)
			}
			if err != nil {
				return err
			}
			fmt.Println("restored", len(pending), "entries")
			return nil
		},
	}
	cmd.Flags().StringVarP(&snap, "snapshot", "s", "", "snapshot name or prefix (default latest)")
	cmd.Flags().StringVar(&to, "to", "", "restore under this directory instead of $HOME")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show the plan without writing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite changed files without keeping a .katbak copy")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

func pruneCmd() *cobra.Command {
	var keep int
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete old snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("keep") {
				cfg, err := e.loadConfig()
				if err != nil {
					return err
				}
				keep = cfg.Keep
			}
			removed, err := snapshot.Prune(e.backupDir, keep)
			for _, s := range removed {
				fmt.Println("pruned", s.Name)
			}
			if err == nil && len(removed) == 0 {
				fmt.Println("nothing to prune")
			}
			return err
		},
	}
	cmd.Flags().IntVarP(&keep, "keep", "k", 0, "snapshots to keep (default from config)")
	return cmd
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Browse snapshots and pick files to restore",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			return tui.Run(e.home, e.backupDir)
		},
	}
}
