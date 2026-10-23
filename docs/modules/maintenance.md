# Maintenance

The VPS under the servers needs tidying too. Docker leaves untagged `<none>` images behind after every image update, the build cache grows, and the journal keeps months of logs. This module cleans those up on a schedule, watches free disk and pending reboots, and can switch on unattended security upgrades.

It never touches a server volume (that is [cleanup](cleanup.md)), a running container, a tagged image or a docker volume.

## What it does

| Task | Removes | Never |
|---|---|---|
| `images` | Dangling (untagged) images that no container uses | `docker image prune -a`: a tagged image nobody runs right now is a rollback target |
| `build-cache` | Build cache older than `build_cache_hours` | Anything newer |
| `journal` | Journal files older than `journal_days` (`journalctl --vacuum-time`) | Newer entries |

Two watches run hourly and only notify:

| Watch | Alert | Why it only alerts |
|---|---|---|
| Free disk on `/` under `disk_warn_percent` | `disk.low`, at most once a day | Deleting to make room is a decision, not a reflex |
| `/var/run/reboot-required` exists | `reboot.required`, once per pending reboot | A reboot drops every player |

Old kernels and `apt autoremove` are not automated. `cs2node doctor` reports a pending reboot.

## Starts as a dry run

`dry_run` is `true` by default. Passes report what they would remove and delete nothing, and dry runs never count toward the totals. Read `cs2node maintenance` after a pass or two, then set `dry_run` to `false`.

## Commands

```bash
sudo cs2node maintenance               # mode, totals per task, the last pass
sudo cs2node maintenance now           # one pass now (a dry run while dry_run is on)
sudo cs2node maintenance now --apply   # one real pass, whatever dry_run says
sudo cs2node maintenance setup         # offer unattended security upgrades
sudo cs2node maintenance setup --yes   # same, without the question
```

## Unattended upgrades

`maintenance setup` checks for the `unattended-upgrades` package, prints exactly what it will do, asks `y/N` (or takes `--yes`), installs the package if it is missing and writes `/etc/apt/apt.conf.d/52cs2node-unattended-upgrades`:

```
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
Unattended-Upgrade::Automatic-Reboot "false";
```

Security upgrades install on their own. The server never reboots by itself: you get a `reboot.required` alert and pick the moment. Delete the file to undo it. Running it again changes nothing. It needs a Debian or Ubuntu host.

## Settings

| Key | Default | What it does |
|---|---|---|
| `every_hours` | `168` | Hours between passes. `0` means only on demand |
| `dry_run` | `true` | Report only, delete nothing |
| `images` | `true` | Prune dangling images |
| `build_cache` | `true` | Prune the build cache |
| `build_cache_hours` | `168` | Keep cache newer than this |
| `journal` | `true` | Trim old journal files |
| `journal_days` | `14` | Days of journal to keep |
| `disk_warn_percent` | `15` | Alert when free disk on `/` drops below this. `0` turns the watch off |

Totals live in `/var/lib/cs2node/maintenance/stats.json`. `cs2node status` shows the next pass and what was freed.

---

[Docs index](../README.md)
