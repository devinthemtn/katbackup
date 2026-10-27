# Upgrading Go

Your Go is at `/usr/bin/go` and is version 1.22.2. That's the version Ubuntu 24.04's apt package installs, which is probably where yours came from. Apt is slow to get new Go versions, so the usual fix is the official download from go.dev.

## Option 1: official tarball (recommended)

```sh
# Remove the apt version so it doesn't shadow the new one
sudo apt remove golang-go golang-1.22-go && sudo apt autoremove

# Download and install the latest release to /usr/local/go
VER=$(curl -s 'https://go.dev/VERSION?m=text' | head -1)   # e.g. go1.26.x
curl -LO "https://go.dev/dl/${VER}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "${VER}.linux-amd64.tar.gz"
rm "${VER}.linux-amd64.tar.gz"
```

Then add it to your PATH in `~/.zshrc`, if it isn't there already:

```sh
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
```

Open a new shell (or run `source ~/.zshrc`) and check with `go version`.

To update later, repeat the download/extract step. Replacing `/usr/local/go` is all it takes.

## Option 2: stay on apt with a newer-Go PPA

```sh
sudo add-apt-repository ppa:longsleep/golang-backports
sudo apt update && sudo apt install golang-go
```

This is simpler, but the PPA can lag a little behind official releases.

## Option 3: don't upgrade at all

Go 1.21 and later download the toolchain a project needs automatically (`GOTOOLCHAIN=auto`). That's why `katbackup` built even though it needs 1.26. The only downsides are the one-time download per new version and `go version` still showing 1.22 outside the project.
