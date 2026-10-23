# Changelog

The bash egg this replaces has its own history at [K4ryuu/CS2-Egg](https://github.com/K4ryuu/CS2-Egg/blob/main/CHANGELOG).

## [1.1.0] - 2026-10-23

### Added

- **maintenance** module: keeps the VPS under the servers tidy. It prunes dangling docker images that no container uses (never tagged images, never `prune -a`), prunes build cache older than a week and trims the journal to 14 days. It watches free disk and a pending reboot and tells you through alerts (`disk.low`, `reboot.required`, `maintenance.done`). It starts as a dry run, so the first passes only report what they would remove. `cs2node maintenance` shows the totals, `maintenance now [--apply]` runs a pass, and `maintenance setup` offers unattended security upgrades without automatic reboot. A setup you already have is left alone. See [Maintenance](docs/modules/maintenance.md).

### Fixed

- The guard blocked a legit player for 6 hours with `reconnect_stuck` after a crash or a few quick map changes. The engine reconnects every player on a map change, which looked exactly like a stuck bot. An address that already reached full signon is now trusted for every rule, including `reconnect_stuck`.

## [1.0.5] - 2026-10-23

### Fixed

- A server that was stopped during a central CS2 update could boot on the old engine and libraries. The game file check only looked at VPK links, which stay valid across an update, so the push was skipped. It now checks every mirrored file (thanks @larnzs, [#4](https://github.com/K4ryuu/CS2-Egg-Go/pull/4)).

## [1.0.4] - 2026-10-14

### Fixed

- `cs2node` wrote about 100 MB to disk every minute, a constant 1.6 MB/s. After each CS2 update check, vpksync copied both `steamclient.so` files into the central install and ran a chmod over every file in it, even when nothing had changed. Both now leave files that are already right alone.
- A daemon restart or self-update posted "Server up" for every server that was already running. Only a server that actually starts while the daemon runs gets one now.

## [1.0.3] - 2026-10-13

### Fixed

- A server running MetaMod:Source never stopped ([#3](https://github.com/K4ryuu/CS2-Egg-Go/issues/3)). Since MetaMod `git1467` its plugin unload can deadlock on shutdown, after the engine is done, and the container stayed up until it was killed from the panel. Once a stop is under way, a server that prints nothing for 2 seconds is now ended.
- The container stops as soon as the server process exits, like `script(1)` did in the bash egg, even if something it left behind still holds the terminal.
- A timed-out stop could leave the engine running behind the egg. `cs2.sh` does not replace itself with the engine, so only the shell was killed. The whole process tree is killed now.

## [1.0.2] - 2026-09-26

### Fixed

- `csgo/backups` could get pushed back below MetaMod on every restart after the first, since the reorder pass mistook it for a stray addon. It now stays anchored ahead of MetaMod for good.
- SteamCMD's `+login` ran before `+force_install_dir`, which triggers a warning and can leave `app_update` in a confused state. `+force_install_dir` now comes first.

### Guard

- `GUARD_ACTION` panel variable now defaults to `block` instead of empty.

## [1.0.1] - 2026-09-25

### Fixed

- The engine's own `backup_round*.txt` snapshots were landing inside MetaMod's addon directory, because MetaMod sat first in the `gameinfo.gi` search path. Every addon update pass now gives them their own `csgo/backups` path ahead of MetaMod.

### Docs

- Community health file links now point at absolute GitHub URLs instead of repo-relative ones, so they resolve correctly wherever they are rendered.

## [1.0.0] - 2026-09-23

The whole thing rewritten in Go. Two binaries, no shell anywhere, and the node side split into modules you switch on one by one.

### The container

- `cs2egg` is the image entrypoint: one static binary, no shell scripts inside. Same boot order, same console format, same config files (migrated in place), same addon updaters and cleanup rules as the bash egg.
- The console filter, the bot guard and the pty handling moved with it. Panel commands echo once, and `quit` stops the container.
- A clean stop no longer prints the engine's `Aborted (core dumped)` line. CS2 exits that way every time; a real crash is still reported.
- The egg reports its map, the name players see and its player count from the console, so the node knows what is happening without a single query packet. The name is the `hostname` convar: the egg asks for it on every map load and falls back to `server.cfg`.
- A command typed in the panel gets a real log line at the new `INPUT` level instead of a bare terminal echo, so it is coloured, levelled, and in the log file with everything else.

### The node

- `cs2node` replaces both bash daemons, their cron jobs and the installer. One binary, `curl` plus `cs2node install`, a wizard that explains each module, and one config file.
- The egg and the node talk over a unix socket in the server's volume. A live connection is the presence signal: no marker files, no timestamp gates, nothing to replay.
- A module that fails to start is logged and dropped. The others keep running.

### Modules

- **vpksync**: one CS2 install per node. Adds a restart policy the bash version did not have: `immediate`, `empty`, or inside a time window, with in-game warnings before a forced restart.
- **guard**: the same 21 nftables rules, now over netlink with no `nft` binary. Same ban tiers, same escalation, same traffic log. Kernel-log blocks reach the server consoles through the socket. The escalation ceiling is a week rather than a day, and a block keeps its reason for as long as it lasts instead of only as long as the rates log holds the line.
- **workshop**: one copy of each workshop item per node instead of one per server. Maps, model packs and skin sets alike, multi-chunk VPKs included. Items move into the node's store on a server's next boot and come back as links, so they stop counting against its disk quota. An item another server already downloaded arrives for free. Versions are kept apart by their Steam manifest, and an item its author updated is handed back so exactly one server fetches the new version.
- **addoncache**: Metamod, CounterStrikeSharp, SwiftlyS2, ModSharp and the .NET runtime fetched from GitHub once per node. Plus version locks: `cs2node pin metamod 2.0.0-git1450` holds the fleet at a build, per server if you want, and rolls servers back if they already updated.
- **alerts**: a Discord webhook for crashes, CS2 updates, held restarts, push failures, guard blocks, backups and node updates. Events are yours to pick, and close ones batch into one message. The posts carry their own name and picture, the facts sit in labelled fields, and a ban reads as `24 hours` rather than `1440 min`. `server.start` and `server.stop` were offered by the old wizard and never sent by anything; they fire now.
- **crashes**: the last 500 console lines, the dumps and logs the crash left behind, and the addon versions, bundled outside the volume. `cs2node crashes`, `cs2node crash <server>`.
- **backup**: a nightly archive per server outside the volumes, of everything you cannot re-download. Include and exclude rules, retention by age and count, `cs2node restore`.
- **metrics**: `cs2node top`, a live view of map, players, cpu, memory and network per server, plus a Prometheus endpoint.
- **cleanup**: the egg's own file cleanup, moved to the node. One rule set for the whole fleet instead of one `cleanup.json` per volume, on a schedule instead of only at boot, and off the boot path so a server no longer waits for a directory walk. Files the server still holds open are left alone, because unlinking those frees no disk until it restarts. `cs2node cleanup` shows what went, per server and per rule.

### Fixed on the way

- `whitelist_steamids` is honoured. It was parsed and never checked.
- A disabled guard rule is inert. It used to fall back to a threshold of 1.
- File logging works when `file_enabled` is true.
- A `Segmentation fault` line is reported as a crash instead of being swallowed.
- The Wings restart call pins Wings' own certificate instead of skipping TLS verification.

### Security

The guard matches its rules against what the engine wrote, not against the player names the engine prints inside its own lines: a crafted name used to forge a rule hit against any address, which the node then dropped at the host firewall. Archives are extracted through a path-restricted root, so a symlink entry cannot be written through. Addon mirrors are off unless an operator sets `ADDON_MIRRORS`, because nothing verifies what a mirror returns and it is loaded as native code.

Every write into a server volume goes through a path-restricted root, so a symlink planted in a volume cannot steer the daemon out of it. Anything a container says over the socket is bounded and stripped of control bytes before a root operator reads it: a server name, a rule name, a map. A container cannot exempt more than its own share of addresses from the guard, cannot grow the offender store without limit, and cannot stall the daemon for the other servers by not reading its socket. Secrets typed into a panel console are redacted before they reach the crash ring, so they never leave the container in a crash bundle. File cleanup runs through the same root on both sides, so no rule and no planted symlink can delete outside the server directory. Addon downloads resolve only to GitHub and the .NET CDN. Crash reports, messages and parallel work are all bounded. Console text is stripped of control characters before an operator sees it. See [the security model](docs/security.md).

### Build and release

Multi-stage Dockerfile, `make` targets for both binaries, GitHub Actions running the tests on every push and publishing releases from `main` and `beta`. The daemon self-updates by channel with a checksum check and keeps the previous binary.

### Licence

MIT to GPL-3.0-or-later. Running it, hosting with it and changing it stay free; handing someone a modified `cs2egg`, `cs2node`, or an image built from them now comes with their source under the same terms. Every source file carries an SPDX header, and `cs2node version` prints the notice. The bash egg keeps its MIT licence in its own repository.

### Retired

`KL-GRD-02`, `KL-GRD-04`, `KL-DMN-05` and `KL-DMN-06` do not exist any more. They described states the file-based protocol could get into, and the socket cannot.
