# CLI reference

Everything `cs2node` can do. Commands that need root re-run themselves under `sudo`, so you rarely have to think about it.

A `<server>` argument is always the panel's server id, which is also the container name. `cs2node status` lists them.

## Node

| Command | Does |
|---|---|
| `cs2node install [--yes] [--channel stable\|beta\|dev]` | The wizard. Run it again any time; current values become the defaults. `--yes` takes every default |
| `cs2node doctor` | Every health check, one line each. Exit 1 when something is actually broken |
| `cs2node status` | Servers with their maps and players, egg connections, push state, firewall, and a section per module |
| `cs2node update` | Check the release channel now and install what is newer |
| `cs2node uninstall [--purge]` | Remove the service and the binary. `--purge` drops the config too |
| `cs2node daemon` | Run the modules. systemd calls this; you do not |
| `cs2node version` | Version, channel, commit, build time |

## Guard

| Command | Does |
|---|---|
| `cs2node blocks` | Active blocks: address, ban length, time left, repeat count, reason |
| `cs2node why <ip>` | Everything known about one address: current block, history, whether it ever joined, whitelist status, recent traffic samples |
| `cs2node block <ip> [minutes]` | Manual block, 15 minutes by default. Never escalated, never expanded |
| `cs2node unblock <ip>` / `unblock all` | Lift one or all |
| `cs2node rates [seconds]` | Sample traffic per source. Joined players are marked |

Private and bridge addresses are refused: blocking `172.18.0.1` would block every proxied client.

## Addon cache

| Command | Does |
|---|---|
| `cs2node pins` | What version every server runs, and the per-server exceptions |
| `cs2node pin <addon> <version> [server]` | Hold every server, or one, at a version. The version is verified on GitHub first |
| `cs2node pin <addon> latest <server>` | Free one server from the global pin |
| `cs2node unpin <addon> [server]` | Follow the newest release again |
| `cs2node unpin all [server]` | Drop every pin, or every pin of one server |

`<addon>` is `metamod`, `css`, `swiftly` or `modsharp`.

## Workshop cache

| Command | Does |
|---|---|
| `cs2node workshop` | Cached items: size, version, how many servers share each, what is out of date |
| `cs2node workshop sync [server]` | Run the share pass for stopped servers |
| `cs2node workshop prune` | Drop versions nothing links to |

## Crashes

| Command | Does |
|---|---|
| `cs2node crashes [server]` | Bundles on disk: when, exit code, map, players, files, size |
| `cs2node crash <server> [n]` | The console tail of the nth newest bundle, newest by default |

## Backups

| Command | Does |
|---|---|
| `cs2node backups [server]` | Archives on disk: when, files, size, how long it took |
| `cs2node backup now [server]` | Archive every running server, or one, right now |
| `cs2node restore <server> [n] [--yes] [--volume /path]` | Unpack a backup over a stopped server's volume |

## Cleanup

| Command | Does |
|---|---|
| `cs2node cleanup [server]` | What the passes removed: per server, per rule, and the total |
| `cs2node cleanup now [server]` | Run a pass over every running server, or one, right now |

## Live view

| Command | Does |
|---|---|
| `cs2node top` | Full screen: map, players, cpu, memory, network per server |

Keys: `q` quits, `s` cycles the sort, arrows and PageUp/PageDown scroll.

## Exit codes

| Code | Means |
|---|---|
| 0 | Fine |
| 1 | The command failed, or `doctor` found a real problem |

`doctor` treats warnings as fine. Only a failure changes the exit code, so it works in a health check without crying wolf.

---

[Docs index](../README.md)
