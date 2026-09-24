# Error codes

Coded messages in the server console. Every one of them prints a hint under it; this page is the longer answer.

```
KitsuneLab | WARN  | [KL-SRV-01] Server crash detected
                     → Review stack trace above for the failing module
```

## Server

### KL-SRV-01: Server crash detected

The engine died while running: a segfault or an abort with a core dump.

Usual causes, in the order worth checking: an addon built for an older CS2, two frameworks that do not get along (CounterStrikeSharp and ModSharp), stale gamedata after a Valve update, or a plugin with a real bug.

With the [crashes module](../modules/crashes.md) on the node, the console tail and the dumps are already saved: `cs2node crash <server>`.

A clean stop is not this. CS2 exits noisily every time and the egg swallows that.

### KL-SRV-02: Steam GSLT token invalid or expired

The server could not log in to Steam. It still runs, but it is not publicly listed.

Regenerate the token at [steamcommunity.com/dev/managegameservers](https://steamcommunity.com/dev/managegameservers) for App ID 730, put it in `STEAM_ACC`, restart. Tokens expire when unused for a long time, and they are tied to one server at a time.

## SteamCMD

### KL-STM-01: Connection error (exit code 8)

SteamCMD could not reach Steam. Usually Steam being down or the node's network. It retries on the next boot; the installed files are untouched.

### KL-STM-02: SteamCMD failed

Any other non-zero exit. The code is in the message. Common ones: disk full, a partially written install, or wrong credentials when you set `SRCDS_LOGIN`.

### KL-STM-05: Failed to download SteamCMD after 3 attempts

The node could not fetch SteamCMD itself. Network, or Valve's CDN having a moment.

### KL-STM-06: Failed to extract SteamCMD

The download arrived broken. Delete `steamcmd/` in the volume and restart.

### KL-STM-07: steamcmd directory does not exist

Something removed it mid-boot. Restart; the egg reinstalls it.

## Node daemon

### KL-DMN-03: Node daemon reported a failed VPK push

The node tried to give this server its game files and could not. The server falls back to its own SteamCMD, so it boots either way.

Look at `journalctl -u cs2node` around that time. Usual causes: the central install is incomplete, the disk is full, or the volume is on a filesystem that cannot take the chosen `push_method`.

### KL-DMN-04: Node said done but no VPK file is readable

The push reported success but the server cannot read a single VPK. Almost always the read-only shared mount missing inside the container: kernel older than 5.2, or the daemon was not running when the container started.

`cs2node doctor` names it directly. Restarting the server usually fixes it; on an old kernel switch `push_method` to `copy`.

## Bot guard

### KL-GRD-01: Bot-guard tripped a rule

Not an error. A client matched one of the rules in `egg/configs/guard.json`. The line says which rule, which address, what the configured action is and how long the ban would be.

If it names a real player, raise that rule's threshold or whitelist them.

### KL-GRD-03: Command channel unverified after 20s

The guard could not confirm that it can type into the server console, so `kick` and `block` actions may not land. Detection still works.

It self-tests by echoing a token and watching for it. Failing means something ate the line: an aggressive console filter pattern, or a plugin rewriting output. Check `console-filter.json` first.

### KL-GRD-05: Host guard dropped an address

Not an error. The node blocked an address for this server, and the client's remaining console lines are muted from here. The reason and the ban length are in the message.

`cs2node why <ip>` on the node has the whole story.

---

[Docs index](../README.md)
