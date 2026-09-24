# Config vs. Variables audit

Every field in the egg's four config files (`egg/configs/*.json`), classified: does it belong on the panel's Startup tab as a Variable, or does it stay file-only. The rule: a Variable if it is a simple type a normal server owner would plausibly want to change, and nothing already covers it. File-only if it is structural (a list or an array of objects), or genuinely expert-only, or would clutter the wizard for what it is worth.

Previously decided: `logging.prefix` is config-only, while `logging.file_enabled` and `logging.max_days` are handled via `ENABLE_LOG_FILES` and node-level retention settings. See [panel variables](panel-variables.md) and [server configs](server-configs.md).

## console-filter.json

| Field | Verdict | Why |
|---|---|---|
| `preview_mode` | File-only | A simple bool on its own, but it is only ever useful while also editing `patterns`, which must stay file-only (below). A Variable that only pays off next to a file you already have open saves nothing; it would just be a second place to look when tuning one feature. |
| `patterns` | File-only | Variable-length list of strings. A comma-separated Variable (this egg's pattern for short lists, see `ADDON_MIRRORS`) does not work here: a filter pattern can legitimately contain a comma as text to match, so comma-joining would corrupt it. |

## cleanup.json

| Field | Verdict | Why |
|---|---|---|
| `rules` | File-only | 8 rules × 6 fields each. `CLEANUP_ENABLED` is already the Variable; exposing every rule field individually would be ~48 wizard entries for a feature whose defaults already cover the common case. |

## logging.json

| Field | Verdict | Why |
|---|---|---|
| `console_level` | **Variable** (`CONSOLE_LOG_LEVEL`) | Simple enum, and the one thing here people reach for constantly: turning on `DEBUG` to see why the egg did something is the most common reason to touch this file at all. |
| `max_size_mb`, `max_files` | File-only | Local file-logging's own internal rotation knobs. Expert tuning, rarely touched once set, and the feature already has sane shipped defaults. Kept file-only for the same reason `max_days` briefly was before it got a node-level Variable: these two stayed local-only and did not move with it. |
| `prefix`, `file_enabled`, `max_days` | Settled already | See above. |

## guard.json

| Field | Verdict | Why |
|---|---|---|
| `action` | **Variable** (`GUARD_ACTION`) | Simple enum (`log` / `kick` / `block`), and the single most common thing an owner adjusts: start in `log` to watch it work, flip to `block` once trusted. |
| `ban_minutes` | **Variable** (`GUARD_BAN_MINUTES`) | Simple int, a normal "how long" knob owners tune once they see how guard behaves on their server. |
| `whitelist_steamids` | **Variable** (`GUARD_WHITELIST_STEAMIDS`) | Short, clean tokens (SteamID64s), no internal commas, so comma-joining is safe. An owner adding their own or a friend's ID is a genuinely common, simple edit. |
| `whitelist_ips` | **Variable** (`GUARD_WHITELIST_IPS`) | Same reasoning as SteamIDs: clean tokens, safe to comma-join, common edit. |
| `cooldown_secs` | File-only | Anti-thrash tuning between two reactions on the same IP. Fine-grained, set once during initial tuning if at all, not a "plausibly want to touch" default case. |
| `stuck_grace_secs` | File-only | Same class as `cooldown_secs`: a timing knob for an edge case, not a day-to-day setting. |
| `behavior_rules` | File-only | 7 rules × 5 fields each (name, threshold, window, ban minutes, enabled, log). Clearly structural; `docs/modules/guard.md` already documents tuning these directly. |

## Wiring for the new Variables

Same shape as the rest of this egg's value-carrying Variables (not a toggle-gate like `ENABLE_FILTER`): the Variable, when set, overrides the config file's value for that boot; an empty or unset Variable leaves the file in charge. Nothing is migrated or written back, unlike `PREFIX_TEXT`, since these were never Variables before, there is no old value to carry forward.

| Variable | Overrides | Empty means |
|---|---|---|
| `CONSOLE_LOG_LEVEL` | `logging.console_level` | Use `logging.json`'s value |
| `GUARD_ACTION` | `guard.action` | Use `guard.json`'s value |
| `GUARD_BAN_MINUTES` | `guard.ban_minutes` | Use `guard.json`'s value |
| `GUARD_WHITELIST_STEAMIDS` | `guard.whitelist_steamids` | Use `guard.json`'s value |
| `GUARD_WHITELIST_IPS` | `guard.whitelist_ips` | Use `guard.json`'s value |

---

[Docs index](../README.md)
