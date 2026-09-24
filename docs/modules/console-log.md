# Console log persistence

Everything the panel's console tab shows, kept past a restart. Without this, a server's history is whatever the panel scrollback still remembers when you look.

The egg tees every line it prints, the game server's own output and its own messages alike, to the node. Masking and the `ENABLE_FILTER` rules already applied to what you see are applied to what gets saved too: a hidden line stays hidden in the file, and the GSLT is never in it.

```
/var/lib/cs2node/logs/<server>/2026-06-15.log
/var/lib/cs2node/logs/<server>/2026-06-14.log.gz
```

One directory per server, one file per day. Every file older than today is gzip-compressed on the daemon's daily sweep. `keep_days` is configurable; a hard 365-day ceiling always applies underneath it, even set to `0` or something absurd, so a misconfigured node can never grow this forever.

## Turning it on

Two switches, both need to be on:

- The panel's **Log File Persistence** variable (`ENABLE_LOG_FILES=1`) on the server itself.
- This module installed on the node the server's egg talks to.

Either alone does nothing. The variable on, module missing: the egg logs one warning and keeps nothing, it never falls back to writing inside the container. The path and retention are node settings, not per-server, so every server on a node shares them.

## Commands

```bash
sudo cs2node logs                 # every server's log files: name, size, age
sudo cs2node logs <server>        # one server
```

## Delivery

Lines go over the same unix socket every other module uses, one message per line, not batched: the queue between the egg's console pipe and the socket write already absorbs a burst, and a persisted log favors per-line durability over shaving syscalls. A node that is slow or gone never blocks the game server itself: the egg's send queue is bounded, and it drops lines (logging how many, on the console) rather than stall the process reading the game's own stdout.

## Config

`modules.consolelog` in `/etc/cs2node/config.json`. Every key: [node config](../reference/node-config.md#consolelog).

---

[Docs index](../README.md)
