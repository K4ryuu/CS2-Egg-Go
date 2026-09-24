# Config files

Four JSON files under `egg/configs/` in the server's volume. They are written on the first boot, read on every boot, and migrated in place when a new version of the egg adds settings.

| File | Controls | Needs |
|---|---|---|
| `console-filter.json` | Which console lines reach the panel | `ENABLE_FILTER=1` |
| `cleanup.json` | What gets deleted, and when | `CLEANUP_ENABLED=1` |
| `logging.json` | Console level and log files | always read |
| `guard.json` | The bot guard's rules | `ENABLE_GUARD=1` |

## How migration works

Each file carries a `version`. When the egg ships a newer template:

- Keys you never touched take the new default.
- Keys you changed keep your value.
- New entries in a rule list are appended; your rules stay, matched by name.
- Rules the template dropped are removed.
- A file that will not parse is left alone, and the egg boots with the defaults plus a warning. It never writes an empty config over a broken one.

So editing these files is safe, and so is an update.

## console-filter.json

```json
{
  "preview_mode": false,
  "patterns": [
    "Certificate expires",
    "@Server is hibernating"
  ]
}
```

A pattern with `@` matches the whole line exactly. Without it, any line containing the text is dropped. `preview_mode` re-emits what was blocked at debug level, so you can see what a new pattern would eat before you trust it. Empty lines always pass, and the GSLT is masked no matter what.

## cleanup.json

A list of independent rules. The defaults cover the usual disk eaters:

| Rule | Deletes |
|---|---|
| `backup_rounds` | `backup_round*.txt` match backups |
| `demos` | `*.dem` older than the age you set |
| `css_logs`, `swiftly_logs` | Framework logs |
| `swiftly_crash_reports`, `accelerator_dumps`, `core_dumps` | Crash output |

```json
{
  "name": "demos",
  "directories": ["./game/csgo"],
  "patterns": ["*.dem"],
  "hours": 168,
  "recursive": true,
  "delete_parent_dir": false,
  "enabled": true
}
```

| Field | Meaning |
|---|---|
| `directories` | Relative to the server root. `.` is the root itself |
| `patterns` | File name globs, not paths |
| `hours` | Older than this. `0` deletes on every run |
| `recursive` | Walk subdirectories |
| `delete_parent_dir` | Delete the matched file's folder, for per-crash bundle directories. Never deletes the rule's own root |
| `enabled` | Turn a rule off without losing it |

Every path is resolved inside the server directory and symlinks are never followed, so a rule cannot walk out of the volume through the shared game files.

Cleanup runs once per boot, before the server starts, and prints what it freed. A node running the [cleanup module](../modules/cleanup.md) takes this over: the same rules, on a schedule, configured once for the whole node, and the server no longer waits for a directory walk to start.

## logging.json

```json
{
  "logging": {
    "console_level": "INFO",
    "prefix": "KitsuneLab",
    "file_enabled": false,
    "max_size_mb": 100,
    "max_files": 30,
    "max_days": 7
  }
}
```

`console_level` is one of `DEBUG`, `INFO`, `WARN`, `ERROR`. Debug shows what the egg decided and why, which is what you want when something is not behaving. The panel's `CONSOLE_LOG_LEVEL` variable overrides this when set, so you do not need to edit the file just to turn debug on.

`prefix` is the text in front of every console line (`KitsuneLab | INFO | ...`). It ships with `KitsuneLab` as the install-time default and is edited here afterwards, no panel variable involved. A server that still has the old `PREFIX_TEXT` panel variable set from before this moved gets it folded in automatically, once, on its next boot.

With `file_enabled` (off by default), everything the panel's console tab shows is also written to `egg/logs/YYYY-MM-DD.log`, one file per day: the egg's own messages and the game server's console output, merged in the order they printed, masked and filtered the same way. On the next boot, every file older than today gets gzip-compressed to `YYYY-MM-DD.log.gz`, then files are dropped by size, count and age (whichever limit is hit first). `max_days` has a hard 365-day ceiling that always applies, even set to `0` or higher, so a misconfigured retention setting can never grow the log directory forever.

## guard.json

Thresholds, ban lengths and the seven detection rules. Documented where it is used: [Guard](../modules/guard.md).

---

[Docs index](../README.md)
