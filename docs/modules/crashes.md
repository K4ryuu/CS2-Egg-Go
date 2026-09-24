# Crash bundles

A CS2 crash takes its evidence with it: the console scrolls away on restart, the minidump sits in a volume nobody looks at, and by the time someone asks, the server has been running fine for a day.

This module catches the moment. The egg keeps the last 500 console lines in memory and sends them to the node when the server dies for a reason other than a stop. The node bundles them with whatever the crash left behind.

```
/var/lib/cs2node/crashes/<server>/20260911-031522.tar.gz
    console.txt      the last 500 lines before the crash
    info.txt         time, exit code, map, players
    versions.txt     the addon versions the egg had installed
    files/...        everything in the volume written in the two minutes around the crash:
                     minidumps, logs, plugin output
```

VPKs, symlinks and the Steam trees are never included, and a single file over 512 MiB is listed but left out, because a full engine core dump would fill the disk on its own.

## Commands

```bash
sudo cs2node crashes                 # every bundle: when, exit code, map, players, files, size
sudo cs2node crashes <server>        # one server
sudo cs2node crash <server>          # the console tail of the newest bundle
sudo cs2node crash <server> 3        # the third newest
```

The console tail is printed with control characters stripped, so a crafted line in a server log cannot repaint your terminal.

## What counts as a crash

A stop from the panel is not one. The egg knows it sent `quit`, so the engine's noisy exit is swallowed rather than reported. A real crash is a `Segmentation fault` or `Aborted (core dumped)` while the server was running, or any non-zero exit nobody asked for.

One bundle per server per minute, so a crash loop cannot fill the disk.

Retention: `keep_count` bundles per server and `keep_days` of age, both configurable, both `0` for unlimited. `cs2node doctor` warns about a server that crashed in the last 24 hours.

---

[Docs index](../README.md)
