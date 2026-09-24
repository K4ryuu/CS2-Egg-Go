# Addon cache

Every server boot asks GitHub for the current Metamod, CounterStrikeSharp, SwiftlyS2 or ModSharp release. Twenty servers restarting after a CS2 update is twenty identical API calls from one address, which is how you hit GitHub's rate limit and see update checks skipped.

With this module the egg asks the node instead. The node fetches a release once, keeps the answer for `refresh_minutes`, downloads a file once, and copies it into the server volume. A server that cannot reach a node goes to GitHub itself, exactly as before.

## Version locks

An upstream release can break your servers overnight. A Metamod build that CounterStrikeSharp cannot load reaches every server that restarts after it. So the cache can hold the fleet at a version you pick, and move it back down if the servers already updated.

```bash
sudo cs2node pins                              # what every server runs, and the exceptions
sudo cs2node pin metamod 2.0.0-git1450         # every server, on their next restart
sudo cs2node pin css v1.0.360
sudo cs2node pin metamod git1400 <server>      # one server on an older build
sudo cs2node pin metamod latest <server>       # this one follows the newest again
sudo cs2node unpin metamod                     # everyone follows the newest again
sudo cs2node unpin all                         # every pin gone
```

A pin is a release tag, or any part of one. For Metamod, whose CS2 builds are all tagged `2.0.0`, it is the build number from the asset name: `git1450` or `2.0.0-git1450`.

`cs2node pin` looks the version up on GitHub before writing it, so a typo is refused instead of quietly holding the fleet on nothing. Changes take effect without restarting the daemon: each server picks up its version when it next boots, downgrade included.

If the node cannot find a pinned version, the server keeps what it has and says so in its console. A pin never falls back to "install the newest".

## Config

`modules.addoncache` in `/etc/cs2node/config.json`:

```json
"addoncache": {
  "enabled": true,
  "dir": "/var/lib/cs2node/addoncache",
  "refresh_minutes": 60,
  "keep_days": 30,
  "pin_versions": true,
  "pin_metamod": "2.0.0-git1450",
  "pin_css": "", "pin_swiftly": "", "pin_modsharp": "",
  "pin_per_server": { "<server id>": { "pin_metamod": "git1400" } }
}
```

`pin_versions: false` keeps the pins in the file but lets every server follow the newest release. A per-server entry set to `""` frees that one server while the rest stay held.

The .NET runtime ModSharp needs goes through the same cache. Only GitHub and the .NET CDN are fetched; anything else a server asks for is refused.

---

[Docs index](../README.md)
