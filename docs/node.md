# Install the node daemon

`cs2node` runs on the machine, not in a container, as root. It is optional: servers work without it. With it, they share one CS2 install, one copy of each workshop map, one addon download, and they get a packet filter in front of the game ports.

## Install

```bash
curl -fsSL https://github.com/K4ryuu/CS2-Egg-Go/releases/latest/download/cs2node_linux_amd64 -o /tmp/cs2node
chmod +x /tmp/cs2node && sudo /tmp/cs2node install
```

The binary copies itself to `/usr/local/bin/cs2node`, writes `/etc/cs2node/config.json`, installs a systemd unit and starts it. The `/tmp` copy can go.

`sudo cs2node install --yes` takes every default without asking. Run `install` again any time to change your mind; the current values become the defaults.

## The wizard

It goes module by module. Each one says what it does, asks whether you want it, then asks only the questions that matter for the answers you gave. When a module is switched on it tells you what changes on your servers.

What most nodes want:

| Module | Say yes when |
|---|---|
| `vpksync` | You have more than one CS2 server. This is the 52 GB per server one |
| `guard` | You get bot floods. Needs kernel 4.18+ |
| `workshop` | Your servers run workshop maps or model packs |
| `addoncache` | You have enough servers to hit GitHub's rate limit, or you want version locks |
| `crashes` | Always. It costs nothing until something crashes |
| `metrics` | You want `cs2node top` |
| `alerts` | You want it in Discord. Needs a webhook URL |
| `backup` | You want nightly archives and have the disk for them |
| `cleanup` | Always. It takes the file cleanup off every server's boot and gives you one rule set |
| `consolelog` | You want a server's console history to survive a restart. Costs disk, one directory per server |

## Check it

```bash
sudo cs2node doctor
```

Every check with its own line, and an exit code you can use in a script. It knows the difference between a problem and a note: a missing IPv6 firewall is a warning, no CS2 install is a failure.

```bash
sudo cs2node status
```

Servers, their maps and player counts, egg connections, push state, the firewall, and a section per module.

## Then restart a server

The modules do their work when a server boots: the file sharing, the workshop pass, the addon answers. Restart one server from the panel and watch its console:

```
KitsuneLab | RUN   | Checking for daemon-managed game files...
KitsuneLab | OK    | Daemon-managed game files, SteamCMD skipped
KitsuneLab | OK    | Workshop: 3 shared, 0 received, 1.2 GB freed in this server
```

## Requirements

| | |
|---|---|
| Root | The config, the control socket and nftables are root-only. Commands re-run themselves under `sudo` when you forget |
| Kernel | 4.18 for the guard, 5.2 for the read-only shared mounts |
| Docker | Already there, Wings needs it |
| Disk | 40 GB for the shared CS2 install, plus whatever workshop content you cache |

## Where it puts things

| Path | What |
|---|---|
| `/usr/local/bin/cs2node` | The binary. The previous one is kept as `cs2node.prev` after a self-update |
| `/etc/cs2node/config.json` | Your config. Root only, never rewritten by an update |
| `/var/lib/cs2node/` | Module state: guard offenders, crash bundles, addon cache |
| `/srv/cs2-shared/` | The shared CS2 install |
| `/srv/cs2-workshop/` | The shared workshop content |
| `/var/log/cs2node/` | The guard's traffic log. Everything else goes to the journal |

Logs: `journalctl -u cs2node -f`.

## Updates

The daemon checks its release channel daily and installs what is newer, after verifying the checksum. `stable` gets stable releases, `beta` is prereleases, `dev` never updates itself.

```bash
sudo cs2node update      # check right now
```

Details and the rollback: [upgrading](upgrading.md).

## Rolling it out

A checklist for putting this on a node that people are playing on. Half an hour, most of it waiting for downloads.

### Before you start

- [ ] A test server you can restart freely.
- [ ] Root on the node, and 40 GB free for the shared CS2 install if you want VPK sync.
- [ ] Kernel 5.2 or newer: `uname -r`. Older is fine, you lose the shared mounts.
- [ ] If you run the bash egg today, read [migrating](migrating-from-bash.md) first.

### 1. One server, no node

Import the egg, point a test server at `ghcr.io/k4ryuu/cs2-egg-go:stable`, start it.

- [ ] It boots and players can join.
- [ ] Your plugins load.
- [ ] Panel console commands work and echo once.
- [ ] Stop from the panel is clean, with no crash reported.

This is the whole egg working without anything on the node. If something is wrong here, the node side will not fix it.

### 2. The node daemon

```bash
curl -fsSL https://github.com/K4ryuu/CS2-Egg-Go/releases/latest/download/cs2node_linux_amd64 -o /tmp/cs2node
chmod +x /tmp/cs2node && sudo /tmp/cs2node install
```

Start with the modules that cannot surprise you: `crashes`, `metrics`, `cleanup`, `addoncache`. Add `vpksync` and `guard` once you have watched them for a day.

- [ ] `sudo cs2node doctor` is clean, or the warnings are ones you understand.
- [ ] `sudo cs2node status` lists your servers with their maps and player counts.

### 3. VPK sync

The first run downloads CS2 on the node, which takes a while. Watch it:

```bash
journalctl -u cs2node -f
```

- [ ] `cs2node doctor` shows a build id and a VPK count for the central install.
- [ ] Restart the test server. Its console says `Daemon-managed game files, SteamCMD skipped`.
- [ ] The server's disk use in the panel dropped.
- [ ] Players can still join and the map loads.

Pick a restart policy before a Valve update finds you: `empty` or `window` if you would rather not drop 20 players mid-round.

### 4. Guard

- [ ] `ENABLE_GUARD=1` on the server, action left on `log`.
- [ ] `block_mode` on the node set to `log` for the first day.
- [ ] After a day: `sudo cs2node blocks` and `/var/log/cs2node/guard-rates.log`. If nothing in there is a real player, switch `block_mode` to `enforce`.

Have `sudo cs2node unblock <ip>` ready. Somebody will ask.

### 5. The rest

- [ ] `workshop`: restart a server that uses workshop content and watch its disk use drop.
- [ ] `backup`: `sudo cs2node backup now` once by hand, check the archive, then leave it to its hour.
- [ ] `alerts`: create the webhook and confirm a real event arrives. Restarting a server produces one if you enabled `server.start`.

### 6. Then roll out

Move servers in batches, not all at once. The node handles a mass restart (pushes queue, and each server sees its place in the queue), but you want a small blast radius the first time.

Leave one server on the old setup for a week if you can. It is a cheap control group.

### Things that bite people

**Wings pulls the image on start, but not always.** If a server does not seem to get a new image, `docker pull` it on the node and stop/start the server from the panel; Restart does not always replace the container.

**`cs2node install` restarts the daemon.** Running servers reconnect on their own within seconds.

**The shared mount appears when a container starts.** A server that was running before you installed the daemon does not have it until its next restart. `cs2node doctor` says so.

## Uninstall

```bash
sudo cs2node uninstall             # service and binary, config kept
sudo cs2node uninstall --purge     # config too
```

The nftables table stays until its blocks expire, so nobody gets un-banned by an uninstall. Drop it by hand with `nft delete table inet cs2guard` if you want it gone now. The shared CS2 install under `/srv` stays; delete it when you are sure no server links to it.

---

[Docs index](README.md)
