// Package cli wires katbackup's cobra commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/paths"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

const passEnv = "KATBACKUP_PASSPHRASE"

// version is set for release builds with
// -ldflags "-X github.com/devinthemtn/katbackup/internal/cli.version=1.2.3".
var version string

// Version is the version this binary reports. Every source of it comes from the
// git tag, so tagging a release is all it takes to update it.
func Version() string {
	var module string
	if bi, ok := debug.ReadBuildInfo(); ok {
		module = bi.Main.Version
	}
	return resolveVersion(version, module, func() string { return gitDescribe(sourceDir()) })
}

// resolveVersion prefers a version stamped at build time, then the one Go
// recorded from the module or git, such as v1.2.3, or v1.2.3+dirty for a build
// with uncommitted changes. `go run` records only "(devel)" or nothing, so it
// asks git, which is only called when needed.
func resolveVersion(stamped, module string, describe func() string) string {
	if stamped != "" {
		return stamped
	}
	if strings.HasPrefix(module, "v") {
		return strings.TrimPrefix(module, "v")
	}
	if v := describe(); v != "" {
		return v
	}
	return "dev"
}

// sourceDir is the folder this file was compiled from, or "" if the build
// removed that path. It's used instead of the working directory, which could be
// some other project's repository.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		return ""
	}
	return filepath.Dir(file)
}

// gitDescribe returns the version of the repository at dir, such as 1.2.3,
// 1.2.3-dirty or 1.2.3-4-gabc1234 for commits after a tag, or "" if unknown.
func gitDescribe(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
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
