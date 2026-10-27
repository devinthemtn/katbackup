// Package cli wires katbackup's cobra commands.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/paths"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

const passEnv = "KATBACKUP_PASSPHRASE"

// version can be overridden at build time with
// -ldflags "-X github.com/devinthemtn/katbackup/internal/cli.version=1.2.3".
var version = "0.1.0"

// Version is the release tag the binary was built from (e.g. via
// `go install ...@v0.2.0` or a build at a tagged commit), falling back to version.
func Version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		v := bi.Main.Version
		// Untagged and dirty builds get pseudo-versions like v0.0.0-2026...-abc123+dirty.
		if strings.HasPrefix(v, "v") && !strings.ContainsAny(v, "-+") {
			return strings.TrimPrefix(v, "v")
		}
	}
	return version
}

func Execute() error {
	v := Version()
	root := &cobra.Command{
		Use:           "katbackup",
		Short:         "Back up and restore your config files into ~/.katConfigBackup",
		Long:          "katbackup " + v + " — back up and restore your config files into ~/.katConfigBackup",
		Version:       v,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		initCmd(), scanCmd(), backupCmd(), listCmd(), showCmd(),
		restoreCmd(), pruneCmd(), tuiCmd(),
	)
	return root.Execute()
}

type env struct {
	home      string
	backupDir string
	cfgPath   string
}

func newEnv() (env, error) {
	home, err := paths.Home()
	if err != nil {
		return env{}, err
	}
	return env{home: home, backupDir: paths.BackupDir(home), cfgPath: paths.ConfigFile(home)}, nil
}

func (e env) loadConfig() (config.Config, error) {
	return config.Load(e.cfgPath)
}

// passphrase reads from $KATBACKUP_PASSPHRASE or prompts on the terminal.
func passphrase(prompt string, confirm bool) (string, error) {
	if p := os.Getenv(passEnv); p != "" {
		return p, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal for passphrase prompt; set $%s", passEnv)
	}
	defer tty.Close()
	read := func(p string) (string, error) {
		fmt.Fprint(tty, p)
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return string(b), err
	}
	p, err := read(prompt)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("empty passphrase")
	}
	if confirm {
		again, err := read("Confirm passphrase: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("passphrases do not match")
		}
	}
	return p, nil
}

// openIndex loads a snapshot's index, prompting for a passphrase if encrypted.
func openIndex(s snapshot.Snapshot) (archive.Index, string, error) {
	var pass string
	if s.Encrypted {
		var err error
		if pass, err = passphrase("Passphrase for "+s.Name+": ", false); err != nil {
			return archive.Index{}, "", err
		}
	}
	idx, err := archive.ReadIndex(s.Path, pass)
	return idx, pass, err
}

func confirm(prompt string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Print(prompt + " [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
