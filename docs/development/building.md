# Building

Go 1.27 and Docker with buildx. Nothing else.

```bash
git clone https://github.com/K4ryuu/CS2-Egg-Go
cd CS2-Egg-Go
make test
```

## Binaries

```bash
make egg      # dist/cs2egg,  linux/amd64, static
make node     # dist/cs2node, linux/amd64, static
```

Both are `CGO_ENABLED=0` with the version, channel, commit and build time stamped in through ldflags. `cs2node version` prints all four, which is how you tell two dev builds apart.

Override anything:

```bash
make node VERSION=2.1.0 CHANNEL=beta
```

## The image

```bash
./scripts/build.sh dev -g          # build and push the :dev tag to GHCR
./scripts/build.sh 1.0.0           # a version tag
```

The Dockerfile is two stages. The first compiles `cs2egg` on the builder's native platform and cross-compiles for linux/amd64. The second flattens the SteamRT runtime into one layer and copies the binary in. No shell scripts, no `jq`, no package manager in the final image.

The base is flattened on purpose: the upstream SteamRT layer stores hardlink targets as absolute paths, and some dockerd versions refuse it on pull with `invalid hardlink target`. Re-exporting the rootfs through BuildKit rewrites them.

## Testing against a node

`make node-dev` builds, copies the binary to a host from `.env.local` and restarts the service:

```bash
echo "NODE_HOST=game2" > .env.local    # gitignored
make node-dev
```

Install it there with the `dev` channel so it never replaces itself:

```bash
ssh game2 'sudo cs2node install --channel dev'
```

## Releases

```bash
./scripts/release.sh              # cut a release for the top CHANGELOG.md entry
./scripts/release.sh --prerelease # mark it beta
./scripts/release.sh --dry-run    # preview the version/notes, touch nothing
```

`release.sh` reads the version from the top `## [X.Y.Z]` entry of `CHANGELOG.md`, builds `cs2node` locally as a fail-fast smoke test (never uploaded), and publishes a GitHub Release for `vX.Y.Z` targeting the current commit, with no assets attached. Publishing that release is what builds the real binary: `release.yml` triggers on it, is the only thing that ever uploads `cs2node_linux_amd64` and `checksums.txt`, and runs the download-and-verify round trip below. There is no release branch: the commit you released from is what gets built, and the release's own prerelease flag decides the channel.

| The release is | Channel | Who installs it |
|---|---|---|
| a normal release | `stable` | Every node on `stable`, and every node on `beta` |
| marked pre-release | `beta` | Only nodes on `beta` |

A tag that looks like a prerelease (`v2.1.0-rc1`) but is not marked as one fails the build rather than installing itself on every stable node.

The `VERSION` file is only for local builds now; the release tag carries the version.

The daemon's self-updater downloads both files, verifies the SHA-256 and only then swaps the binary. Both come straight from GitHub over TLS: the addon downloader's community mirrors are deliberately not used here, because a mirror that serves the binary can serve a `checksums.txt` to match it.

## CI

Every push and every pull request runs `go vet`, `go test ./...` and a linux/amd64 build. That is the whole gate; keep it green.

---

[Docs index](../README.md)
