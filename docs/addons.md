# Addon frameworks

Metamod, CounterStrikeSharp, SwiftlyS2 and ModSharp install and update themselves on every boot while their panel switch is on. Nothing to download by hand, nothing to unpack.

| Framework          | Panel variable     | Source                                                                            |
| ------------------ | ------------------ | --------------------------------------------------------------------------------- |
| Metamod:Source     | `INSTALL_METAMOD`  | [alliedmodders/metamod-source](https://github.com/alliedmodders/metamod-source)   |
| CounterStrikeSharp | `INSTALL_CSS`      | [roflmuffin/CounterStrikeSharp](https://github.com/roflmuffin/CounterStrikeSharp) |
| SwiftlyS2          | `INSTALL_SWIFTLY`  | [swiftly-solution/swiftlys2](https://github.com/swiftly-solution/swiftlys2)       |
| ModSharp           | `INSTALL_MODSHARP` | [Kxnrl/modsharp-public](https://github.com/Kxnrl/modsharp-public)                 |

## What happens on boot

For each enabled framework the egg asks GitHub for the current release, compares it with `egg/versions.txt`, and installs when they differ.

```
KitsuneLab | INFO  | Update available for CSS: v1.0.374 (current: v1.0.371)
KitsuneLab | OK    | CounterStrikeSharp updated to v1.0.374
```

Rules it follows:

- An installed version newer than the release is left alone, so a manual upgrade is not undone. Exception: with `PRERELEASE` off, an installed prerelease goes back to the latest stable.
- `versions.txt` is only written after the files are in place. A failed copy is retried next boot instead of being recorded as done.
- GitHub answering 403 or 429 means rate limited: the check is skipped for this boot, the installed version keeps running, and the console says so. With the [addon cache](modules/addon-cache.md) on the node, this stops happening.
- SwiftlyS2 over an existing install refreshes `bin/` and `gamedata/` only, so your configs and plugins survive.
- ModSharp keeps `configs/core.json` and `admins.jsonc` across updates, and installs the .NET runtime its core was built for (read from `Sharp.Core.runtimeconfig.json`).

## gameinfo.gi

The egg edits `game/csgo/gameinfo.gi` to mount what you enabled, and keeps Metamod first in the search paths, which is what CounterStrikeSharp needs. The file is backed up, verified after the edit, and restored if the edit went wrong.

The egg adds the missing lines of enabled frameworks and leaves other lines alone, your own included. The one exception is ModSharp, see [Compatibility](#compatibility). A CS2 update or `SRCDS_VALIDATE` can bring back Valve's file, then only the enabled frameworks come back.

`ALLOW_TOKENLESS` also lives here: it flips `RequireLoginForDedicatedServers`.

## Compatibility

|                    | Works with         | Does not                      |
| ------------------ | ------------------ | ----------------------------- |
| CounterStrikeSharp | Metamod, SwiftlyS2 | ModSharp                      |
| SwiftlyS2          | CounterStrikeSharp | ModSharp                      |
| ModSharp           | Metamod            | CounterStrikeSharp, SwiftlyS2 |

ModSharp runs alone. While it is on, the egg keeps CounterStrikeSharp and SwiftlyS2 unloaded: SwiftlyS2 comes out of `gameinfo.gi`, and `counterstrikesharp.vdf` becomes `counterstrikesharp.vdf.disabled` so MetaMod skips it. Files and configs stay. Turn ModSharp off with either of them on, and the egg swaps back: they load again, ModSharp comes out of `gameinfo.gi`.

## Holding a version

When an upstream release breaks something, the [addon cache](modules/addon-cache.md) on the node pins every server to a version you choose, and rolls them back if they already updated:

```bash
sudo cs2node pin metamod 2.0.0-git1450
```

Without a node, set `SRCDS_STOP_UPDATE` or turn the framework's switch off for a boot.

---

[Docs index](README.md)
