# Troubleshooting

Symptom first. If you are not sure which side is wrong, start with `sudo cs2node doctor` on the node and the server's console in the panel.

## The server

### It downloads CS2 even though the node manages the files

The console says `Daemon-managed game files, SteamCMD skipped` when sharing works. If it does not:

- Is the daemon running? `systemctl status cs2node`
- Does the node see the container? `cs2node status` lists it, with its egg connection.
- Is the image name in `modules.images`? A server on an image the node does not recognise is ignored by every module.
- `KL-DMN-03` or `KL-DMN-04` in the console has the specific reason. See [error codes](reference/error-codes.md).

### The console shows a crash after I pressed Stop

It should not: the engine's noisy exit on shutdown is swallowed. If you see `KL-SRV-01` after a stop, the image is older than 2026-09-11. Stop and start the server so Wings pulls the current one.

### Map and player counts show `-` in `cs2node status`

The egg has not reported yet. Give it a few seconds after a boot. If it stays:

```bash
cs2node status      # the egg column shows its build stamp, or "(old image)"
cs2node doctor      # says outright when a container runs a pre-2026-09-11 egg
```

A container that never picked up a new image is the usual answer. Stop and start it.

### A plugin stopped working after an update

Check what changed: `egg/versions.txt` on the server records every framework version the egg installed. With the [addon cache](modules/addon-cache.md) you can put the fleet back:

```bash
sudo cs2node pin metamod 2.0.0-git1450
```

Servers move to that version, downgrade included, on their next restart.

### The console is unreadable

`ENABLE_FILTER=1` and add patterns to `egg/configs/console-filter.json`. Set `preview_mode` to `true` first so blocked lines still show at debug level, and you can see what you are about to lose.

## The node

### `cs2node doctor` says the daemon is not answering

```bash
systemctl status cs2node
journalctl -u cs2node -n 50
```

A module that fails to start does not take the daemon down; it is logged and dropped. The journal says which one and why.

### Doctor says the daemon is a different build from the binary

You copied a new binary but did not restart the service:

```bash
sudo systemctl restart cs2node
```

Dev builds all carry the same version number, so `doctor` compares build stamps too.

### `shared dir not mounted` on a server

The read-only mount is missing inside that container. Either the kernel is older than 5.2, or the daemon was not running when the container started. Restart the server. On an old kernel, switch `push_method` to `copy`.

### A real player got blocked

```bash
sudo cs2node why <ip>        # what tripped, when, how often
sudo cs2node unblock <ip>
```

Then raise that rule's threshold, or add them to `whitelist_ips`. If it keeps happening on one rule, put `block_mode` back to `log` for a day and read `/var/log/cs2node/guard-rates.log`.

### Nothing is being blocked

- Is the module on? `cs2node status` has a Host guard section.
- `block_mode` on `log` reports without blocking. That is the default for a reason; switch it to `enforce` when you are happy.
- The egg's own detections need `ENABLE_GUARD=1` on the server.

### The workshop cache does not seem to do anything

It works on a server's next boot, never on a running one. `cs2node workshop` lists what is cached and how many servers share each item. `cs2node workshop sync <server>` runs the pass by hand for a stopped server.

### A backup or restore failed

`cs2node backups` shows what exists. `restore` refuses a running server, and refuses a volume it cannot find; pass `--volume /path` for a non-standard layout. The journal has the underlying error.

## Still stuck

Open an issue with:

- The server console around the problem, not just the last line.
- `sudo cs2node doctor` output.
- `journalctl -u cs2node -n 100` if the node is involved.
- `cs2node version` and the image tag the server runs.

That is usually enough to answer without a second round of questions.

---

[Docs index](README.md)
