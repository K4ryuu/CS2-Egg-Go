# Security model

`cs2node` runs as root on the node. Each server runs as an unprivileged user who owns their volume and can run arbitrary code inside their container: plugins, custom binaries, anything. The socket the daemon opens is in that volume.

So the rule is simple: everything that comes from a container is input, never instruction.

## What a container user can do

- Write anything into their own volume, including symlinks pointing anywhere on the host.
- Send any message on the socket, at any rate, with any content.
- Print anything to the console, including terminal escape sequences.

## What the daemon does about it

**Every write into a volume goes through a path-restricted root.** The VPK push, the addon delivery, the workshop links, the backup restore, the socket itself. A symlink planted in the volume cannot redirect any of them: the operation fails for that server and the others carry on. This is Go's `os.Root`, which resolves every path component inside the volume and refuses to leave it.

**Reads too.** A backup or a crash bundle reading through a symlink would copy a host file into an archive the server owner can download. Same restricted root, same refusal.

**Nothing from a container picks a destination.** An addon query names a repository or a URL, and only GitHub and the .NET CDN are ever fetched. A workshop item is identified by its numeric id. Nothing in a message becomes a path on the host.

**Shared files are never shared writable.** The CS2 install and the workshop store are mounted read-only, and delivered addon files are copies, not hardlinks, so one server cannot modify what another server reads.

**Everything is bounded.** One crash bundle per server per minute, 2 GiB of files per bundle, eight addon answers at a time, 1 MiB per message, 256 pending notices. A server that misbehaves slows itself down, not the node.

**Console output is sanitised before an operator sees it.** `cs2node crash` strips control characters, so a crafted log line cannot repaint your terminal.

## What the guard touches

Only the game ports of the CS2 containers, in its own nftables table `inet cs2guard`. It does not write to `filter`, `nat` or anything else. SSH, the panel, Wings and every other service are untouched by design, and an uninstall leaves the table in place so nobody gets un-banned by accident.

Private addresses and the Docker bridge can never be blocked, by any path, including a manual `cs2node block`. On a proxied setup the bridge gateway fronts every client, so blocking it would take out everyone.

## What it needs root for

| Why | What |
|---|---|
| nftables | Netlink, no `nft` binary, no shell |
| Mount namespaces | Attaching the read-only shared directories into containers |
| Volumes | Reading and writing server volumes, and chowning to the volume's owner |
| Docker | The container list and events |

There is no shell execution anywhere in the daemon except SteamCMD, which is invoked with an argument list, never a command string.

## Updates

A release is a binary plus `checksums.txt`. Both are downloaded, the SHA-256 is checked, and only then is the binary swapped in, atomically, keeping the previous one as `cs2node.prev`. A mismatch is refused and nothing changes. Nothing inside a release can skip that check.

## Reporting something

Found a hole? [SECURITY.md](SECURITY.md) has where to send it. Please do not open a public issue for anything that would let one server reach another.

---

[Docs index](README.md)
