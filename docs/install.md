# Install a server

Five minutes, all of it in the panel. The node daemon is a separate, optional step: [install the node daemon](node.md).

## 1. Import the egg

Panel > Admin > Nests > Import Egg, from this URL:

```
https://raw.githubusercontent.com/K4ryuu/CS2-Egg-Go/main/pterodactyl/kitsunelab-cs2-go-egg.json
```

It imports as **KitsuneLab CS2 Go**, next to the bash egg if you have that one. They do not clash: different egg, different image, different UUID.

No node daemon anywhere and never planning one? Import
[kitsunelab-cs2-go-egg-standalone.json](https://raw.githubusercontent.com/K4ryuu/CS2-Egg-Go/main/pterodactyl/kitsunelab-cs2-go-egg-standalone.json)
instead: the same egg, minus the Variables that only do anything with a node (currently just Log File Persistence), so the wizard does not show a switch that would silently do nothing. Its own UUID, so both can be imported side by side if you are not sure yet.

If you already customised an egg and do not want an import to overwrite it, add the variables you are missing by hand instead. The names are the same as the bash egg's, plus `ENABLE_GUARD`.

## 2. Create the server

Nothing unusual. Give it the ports you normally would; CS2 needs the game port and, if you use SourceTV, one more.

| Resource | Suggested |
|---|---|
| Disk | 40 GB, or 5 GB with [VPK sync](modules/vpk-sync.md) on the node |
| Memory | 2 GB for casual, 4 GB with plugins and a big workshop map |
| CPU | One core per 10 players is plenty; CS2 is single threaded for the tick |

## 3. Pick the image

Startup tab > **Docker Image**:

| Image | For |
|---|---|
| `ghcr.io/k4ryuu/cs2-egg-go:stable` | Production |
| `ghcr.io/k4ryuu/cs2-egg-go:beta` | Testing what comes next |

## 4. Start it

The console walks through the boot in order:

```
KitsuneLab | INFO  | Starting KitsuneLab CS2 Egg
KitsuneLab | OK    | Configs loaded
KitsuneLab | RUN   | Checking for daemon-managed game files...
KitsuneLab | RUN   | Updating server files via SteamCMD...
KitsuneLab | OK    | Metamod is up-to-date (git1467)
KitsuneLab | OK    | Console filter active: 8 patterns
KitsuneLab | INFO  | Starting server: ./game/cs2.sh -dedicated ...
```

The first boot downloads CS2, so it takes a while. Later boots only check for updates.

`Checking for daemon-managed game files...` shows on every boot, node or not: it is the egg asking whether [VPK sync](modules/vpk-sync.md) on the node owns this server's files. No node, or the node says no: the egg falls straight to its own SteamCMD update, same as always. With it: SteamCMD is skipped and the files come from the node's shared install instead. There is no Variable for this, it is detected automatically.

## What to switch on

Everything is off by default. The switches that matter, all on the Startup tab:

| Variable | Turn it on when |
|---|---|
| `INSTALL_METAMOD` | You run any plugin at all |
| `INSTALL_CSS` | CounterStrikeSharp plugins. Pulls Metamod in with it |
| `INSTALL_SWIFTLY` / `INSTALL_MODSHARP` | You use those frameworks instead |
| `ENABLE_GUARD` | Advertising and console-spam bots bother you. See [Guard](modules/guard.md) |
| `ENABLE_FILTER` | The console is too noisy. Rules live in `egg/configs/console-filter.json` |
| `CLEANUP_ENABLED` | Demos, dumps and logs fill the disk |
| `ENABLE_LOG_FILES` | You want the console tab's history kept past a restart. Needs the node's [console-log module](modules/console-log.md), does nothing without it |
| `STEAM_ACC` | You have a GSLT. Without one the server is limited to LAN unless `ALLOW_TOKENLESS` is on |

The full list with defaults and gotchas: [panel variables](reference/panel-variables.md).

## Reading the console

Every line the egg prints has the same shape:

```
KitsuneLab | OK    | Metamod is up-to-date (git1467)
 └ prefix    └ level └ message
```

The prefix is `logging.prefix` in `logging.json` (`KitsuneLab` by default), the level is padded so the messages line up, and both are coloured. Lines from the game itself are passed through untouched.

### Levels

| Level | Colour | Used for |
|---|---|---|
| `DEBUG` | grey | What the egg decided and why. Hidden unless `console_level` is `DEBUG` |
| `INFO` | cyan | What it is about to do |
| `OK` | green | What worked |
| `RUN` | yellow | Something long is in progress |
| `WARN` | yellow | Worth reading, not fatal |
| `GUARD` | magenta | The bot guard and the node's blocks, so security events never hide in the noise |
| `INPUT` | blue | A command someone typed into the panel console |
| `ERROR` | red | It failed |

Set the level in `egg/configs/logging.json`.

A typed command used to come back as a bare echo from the server's terminal: no level, no colour, and nothing in the log file. It gets a real line now, the echo is dropped, and with file logging on there is a record of who asked the server to do what.

```
KitsuneLab | INPUT | changelevel de_dust2
```

### Coded messages

Anything that needs a human decision gets a code, a hint and a link:

```
KitsuneLab | WARN  | [KL-SRV-01] Server crash detected
                     → Review stack trace above for the failing module
                     → Common causes: outdated addons, plugin incompatibility, stale gamedata
```

Every code with its cause and fix: [error codes](reference/error-codes.md).

### What is hidden for you

- Your GSLT, always, in every line and every log file.
- The engine's noisy exit on a clean stop. CS2 ends with `Aborted (core dumped)` or a segfault every single time, and printing that after you pressed Stop only looks like a crash. A crash while the server is running is still reported as one.
- Anything your filter patterns match, when `ENABLE_FILTER=1`.
- The remaining lines of a client the bot guard just blocked.

### Typing into the console

The panel console goes straight to the server, one echo per command, the way you expect. The node can type into it too, which is how a deferred restart warns players.

## Where things live

Inside the container, everything is under `/home/container`:

```
game/                     the CS2 install
  csgo/addons/            Metamod, CounterStrikeSharp, SwiftlyS2
  csgo/cfg/               your server configs
egg/
  configs/*.json          filter, cleanup, logging, guard
  versions.txt            what the addon updaters installed
  logs/                   console logs, when file logging is on
  cs2node.sock            the node daemon's socket, when there is one
```

Your files are yours. The egg writes `egg/`, updates addons, and never touches `csgo/cfg`.

## Next

- Running more than one server on the machine? [Install the node daemon](node.md) and save the disk.
- Coming from the bash egg? [Migration guide](migrating-from-bash.md).
- Something looks wrong? [Troubleshooting](troubleshooting.md).

---

[Docs index](README.md)
