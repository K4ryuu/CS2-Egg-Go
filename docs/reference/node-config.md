# Node config

`/etc/cs2node/config.json`, root only, written by `cs2node install` and never rewritten by an update. Edit it by hand if you prefer, then `systemctl restart cs2node`.

```json
{
  "version": 1,
  "channel": "stable",
  "images": ["ghcr.io/k4ryuu/cs2-egg-go", "sples1/k4ryuu-cs2:dev", "ghcr.io/k4ryuu/cs2-egg:dev"],
  "update": { "auto": true },
  "modules": {
    "vpksync": { "enabled": true, "...": "..." },
    "guard":   { "enabled": true, "...": "..." }
  }
}
```

| Key | Default | Meaning |
|---|---|---|
| `version` | `1` | Config schema. A newer daemon migrates the file on load; you never rewrite it |
| `channel` | `stable` | `stable`, `beta` or `dev`. See [upgrading](../upgrading.md) |
| `images` | the published images, plus the old repo's `:dev` tag during the migration | A container whose image name contains one of these is managed. A name without a tag matches every tag; add the tag to match one. The bash egg's stable tags are deliberately absent |
| `update.auto` | `true` | Daily self-update check. Ignored on `dev` |
| `modules` | | One section per module, each with its own `enabled` |

## vpksync

| Key | Default | Meaning |
|---|---|---|
| `cs2_dir` | `/srv/cs2-shared` | The one full CS2 install. Needs about 40 GB |
| `steamcmd_dir` | `/root/steamcmd` | Installed on first use |
| `push_method` | `symlink` | `symlink`, `hardlink` or `copy`. See [VPK sync](../modules/vpk-sync.md#delivery-methods) |
| `max_workers` | `8` | Parallel pushes. The rest queue, and their servers see their position |
| `auto_restart` | `true` | Restart servers through the Wings API after a CS2 update |
| `restart_policy` | `immediate` | `immediate`, `empty` or `window` |
| `restart_window` | `04:00-06:00` | Local time span for the `window` policy. May cross midnight |
| `max_delay_hours` | `12` | Restart a held server anyway after this. `0` never forces |
| `warn_minutes` | `5` | In-game warnings before a forced restart. `0` for none |
| `validate` | `false` | Run SteamCMD with `validate` on every check. Slow |
| `check_minutes` | `1` | Minutes between CS2 update checks |
| `wings_config` | auto | Override when Wings' `config.yml` is not in the usual place |

## guard

| Key | Default | Meaning |
|---|---|---|
| `block_mode` | `enforce` | `log` reports what it would block without blocking. The static rules always drop |
| `udp_pps_limit` | `1500` | Packets a second from one source before it is blocked |
| `rcon_syn_per_minute` | `20` | Connection attempts a minute before an rcon block |
| `a2s_pps_per_source` | `20` | A2S queries a second. Rate limited, never blocked |
| `tcp_pps_per_source` | `200` | Same for TCP |
| `stranger_pps_per_port` | `3000` | Traffic from addresses that never joined |
| `idle_pps_max` | `15` | A joined client under this is a candidate for the idle check |
| `idle_bytes_min` | `100` | Bytes per packet under this looks like a bot, not a player |
| `idle_grace_secs` | `5` | Wait this long after a join before judging |
| `idle_sample_secs` | `5` | Sampling period |
| `idle_strikes` | `2` | Samples in a row before blocking |
| `idle_ban_minutes` | `60` | Ban for an idle client |
| `chat_early_secs` | `10` | Chat sooner than this after joining is suspicious |
| `repeat_window_hours` | `24` | Repeats inside this window escalate the ban |
| `max_ban_minutes` | `10080` | Cap for any escalation, a week by default. The third offence inside the window lands here |
| `whitelist_ips` | empty | Comma separated. Never blocked |
| `quiet_rules` | empty | Rules whose blocks are not reported into server consoles |
| `rates_log` | `/var/log/cs2node/guard-rates.log` | Traffic samples and blocks. Rotated by size |
| `rates_log_max_sources` | `500` | Sources per sample line |
| `docker_bridge_subnet` | detected | Always allowed. Detected from the panel's docker network at install |

## workshop

| Key | Default | Meaning |
|---|---|---|
| `dir` | `/srv/cs2-workshop` | The node's copy of every item. Same filesystem as the volumes makes absorbing instant |
| `mount` | `/tmp/cs2-workshop` | Where that directory appears inside every container |
| `seed` | `true` | Offer every cached item to every server |
| `keep_days` | `30` | Drop a version nothing links to after this. `0` keeps them |
| `check_minutes` | `360` | How often Steam is asked which version is current |

## addoncache

| Key | Default | Meaning |
|---|---|---|
| `dir` | `/var/lib/cs2node/addoncache` | Release answers and downloaded files |
| `refresh_minutes` | `60` | How long a release answer stays fresh |
| `keep_days` | `30` | Drop files nobody asked for this long |
| `pin_versions` | `false` | Hold every server at the pinned versions below |
| `pin_metamod`, `pin_css`, `pin_swiftly`, `pin_modsharp` | empty | The version to hold. Empty means newest |
| `pin_per_server` | empty | `{"<server id>": {"pin_metamod": "git1400"}}`. An empty value frees that one server |

## alerts

| Key | Default | Meaning |
|---|---|---|
| `webhook` | | The Discord webhook URL. Must be https |
| `events` | `["all"]` | Which notices to post. See [alerts](../modules/alerts.md#events) |
| `name` | hostname | Shown in the message footer, so several nodes can share a channel |
| `username` | `cs2node` | The name the posts appear under, whatever the webhook was called when it was made |
| `avatar_url` | | An https link to a picture for those posts. Empty keeps the webhook's own |

## crashes

| Key | Default | Meaning |
|---|---|---|
| `dir` | `/var/lib/cs2node/crashes` | One subdirectory per server |
| `keep_count` | `20` | Bundles per server. `0` is unlimited |
| `keep_days` | `30` | Age limit. `0` is unlimited |

## consolelog

| Key | Default | Meaning |
|---|---|---|
| `dir` | `/var/lib/cs2node/logs` | One subdirectory per server |
| `keep_days` | `30` | Age limit. Always capped at 365 regardless, see [console log](../modules/console-log.md) |

## backup

| Key | Default | Meaning |
|---|---|---|
| `dir` | `/srv/cs2-backups` | One subdirectory per server |
| `hour` | `5` | Local hour of the daily run |
| `keep_days` | `14` | Age limit. `0` is unlimited |
| `keep_count` | `7` | Archives per server. `0` is unlimited |
| `include` | `["all"]` | Volume-relative paths or globs |
| `exclude` | the sensible list | Globs matching a path, a file name, or a parent directory |

## metrics

| Key | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:9151` | The Prometheus endpoint. Put it on `0.0.0.0` only behind a firewall |
| `sample_seconds` | `10` | How often docker is sampled for the endpoint. `cs2node top` streams at one second |

## cleanup

| Key | Default | Meaning |
|---|---|---|
| `every_hours` | `6` | Hours between passes. `0` runs only on server start |
| `on_start` | `true` | Also clean when a container comes up |
| `keep_open` | `true` | Leave files the server still holds open, since deleting those frees nothing until it restarts |
| `rules` | the egg's eight | The rule set, written out at install. Fields in [the module page](../modules/cleanup.md#rules) |

Paths in `directories` are relative to the volume root, and an absolute path is refused unless it names the root itself (`/home/container`, which is how older `cleanup.json` files wrote it).

## Validation

Every module checks its own section when the daemon starts. A bad value is a startup error for that module with the reason in the journal, not a silent fallback, and the other modules keep running.

---

[Docs index](../README.md)
