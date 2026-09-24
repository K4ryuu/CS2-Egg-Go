# Backups

A nightly archive of the part of a server you cannot re-download: plugins, configs, plugin data, `gameinfo.gi`. Kept outside the volumes, so a server that wipes itself does not take its backups with it.

By default everything goes in except:

- `*.vpk` and symlinks, which the shared CS2 install provides
- any file the central install has at the same path and size
- `steamapps`, `Steam`, `steamcmd`, `.steam`, `temps`, `egg/cache`
- `*.dem` demos

## Commands

```bash
sudo cs2node backups                 # every backup: when, files, size, how long it took
sudo cs2node backup now              # every running server, right now
sudo cs2node backup now <server>     # one server
sudo cs2node restore <server>        # unpack the newest backup over the volume, asks first
sudo cs2node restore <server> 2 --yes
```

`restore` refuses a running server. Stop it in the panel, restore, start. Files in the backup overwrite the volume's copies and nothing is deleted, so a restore never loses work that is not in the archive.

The volume is found under `/var/lib/pterodactyl/volumes` or `/var/lib/pelican/volumes`; pass `--volume /path` for anything else.

## Disk

The module refuses to start an archive that would leave less than a gigabyte free under `dir`. It logs what it needed, what was free, and raises a `backup.failed` notice instead of filling the node's disk at five in the morning.

`cs2node doctor` says the same thing ahead of time: it adds up what the rules would include across every server and compares that against the free space.

Two volumes of 9 and 15 GB with 20 GB free is exactly the case this catches. Narrow `include` to what you cannot re-download, lower `keep_count`, or put `dir` on a bigger filesystem.

## Rules

`include` and `exclude` are volume-relative directories or globs. A pattern matches the whole path, the file name, or a parent directory.

```json
"backup": {
  "dir": "/srv/cs2-backups",
  "hour": 5,
  "keep_days": 14,
  "keep_count": 7,
  "include": ["all"],
  "exclude": ["*.vpk", "*.dem", "steamapps", "Steam", "steamcmd", ".steam", "temps", "egg/cache"]
}
```

Configs and addons only:

```json
"include": ["game/csgo/cfg", "game/csgo/addons"]
```

A file that shrinks while it is being read is padded and listed under `changed`, so the archive is always valid even if a plugin rotates a log mid-backup.

---

[Docs index](../README.md)
