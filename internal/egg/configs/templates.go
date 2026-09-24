// SPDX-License-Identifier: GPL-3.0-or-later

package configs

// SchemaVersion is the config schema the egg writes. Bump it when a template
// changes; existing files migrate on the next boot.
const SchemaVersion = "1.2.6"

const filterTemplate = `{
  "version": "0.0.0",
  "_description": [
    "Console Filter Configuration",
    "",
    "Filter unwanted console messages from CS2 server output.",
    "",
    "Settings:",
    "  - preview_mode: Show blocked messages in debug log (true/false)",
    "  - patterns: Array of filter patterns",
    "",
    "Pattern Matching:",
    "  - Prefix with @ for exact match: \"@Server is hibernating\"",
    "  - Without @ for contains match: \"edicts used\"",
    "",
    "Examples:",
    "  \"@exact text\" - Only blocks lines that match exactly",
    "  \"contains this\" - Blocks any line containing this text",
    "",
    "Note: STEAM_ACC token is automatically masked if set.",
    "",
    "Enable this feature by setting ENABLE_FILTER=1 in the Pterodactyl egg.",
    "",
    "Config location: /home/container/egg/configs/console-filter.json"
  ],
  "preview_mode": false,
  "patterns": [
    "Certificate expires"
  ]
}`

const cleanupTemplate = `{
  "version": "0.0.0",
  "_description": [
    "Cleanup Configuration: rule-based",
    "",
    "Every entry in 'rules' is an independent cleanup target. You can edit,",
    "disable, add, or remove rules without touching any code.",
    "",
    "Rule fields:",
    "  - name: Stat category shown in log output (e.g. 'demos', 'backup_rounds')",
    "  - description: Free-text comment (ignored by the engine)",
    "  - directories: Paths to search, relative to the server root ('.' is the root itself)",
    "  - patterns: Array of filename globs (e.g. '*.dem', 'core.[0-9]*')",
    "  - hours: File must be older than this many hours (0 = delete on every run)",
    "  - recursive: true = walk subdirectories, false = only the directory root",
    "  - delete_parent_dir: true = delete the matched file's whole parent folder",
    "    (for per-crash bundle dirs; the rule's root directory is never deleted)",
    "  - enabled: false disables the rule without deleting it",
    "",
    "Enable cleanup by setting CLEANUP_ENABLED=1 in the Pterodactyl egg.",
    "",
    "Config location: /home/container/egg/configs/cleanup.json"
  ],
  "rules": [
    {
      "name": "backup_rounds",
      "description": "CS2 match backup round snapshots",
      "directories": ["./game/csgo"],
      "patterns": ["backup_round*.txt"],
      "hours": 24,
      "recursive": true,
      "enabled": true
    },
    {
      "name": "demos",
      "description": "SourceTV demo recordings",
      "directories": ["./game/csgo"],
      "patterns": ["*.dem"],
      "hours": 168,
      "recursive": true,
      "enabled": true
    },
    {
      "name": "css_logs",
      "description": "CounterStrikeSharp log files",
      "directories": ["./game/csgo/addons/counterstrikesharp/logs"],
      "patterns": ["*.txt"],
      "hours": 72,
      "recursive": true,
      "enabled": true
    },
    {
      "name": "swiftly_logs",
      "description": "SwiftlyS2 log files",
      "directories": ["./game/csgo/addons/swiftlys2/logs"],
      "patterns": ["*.log"],
      "hours": 72,
      "recursive": true,
      "enabled": true
    },
    {
      "name": "swiftly_crash_reports",
      "description": "SwiftlyS2 crash reports: loose .dmp files and per-UUID bundle dirs",
      "directories": ["./game/csgo/addons/swiftlys2/dumps/crashreport"],
      "patterns": ["*.dmp"],
      "hours": 168,
      "recursive": true,
      "delete_parent_dir": true,
      "enabled": true
    },
    {
      "name": "swiftly_prevention_logs",
      "description": "SwiftlyS2 crash prevention incident logs",
      "directories": ["./game/csgo/addons/swiftlys2/dumps/prevention"],
      "patterns": ["*.log"],
      "hours": 168,
      "recursive": false,
      "enabled": true
    },
    {
      "name": "accelerator_dumps",
      "description": "AcceleratorCS2 crash dumps and reports",
      "directories": ["./game/csgo/addons/AcceleratorCS2/dumps"],
      "patterns": ["*.dmp", "*.dmp.txt"],
      "hours": 168,
      "recursive": true,
      "enabled": true
    },
    {
      "name": "core_dumps",
      "description": "Linux core dumps (delete on every run)",
      "directories": ["./game/bin/linuxsteamrt64", "."],
      "patterns": ["core", "core.[0-9]*"],
      "hours": 0,
      "recursive": false,
      "enabled": true
    }
  ]
}`

const loggingTemplate = `{
  "version": "0.0.0",
  "_description": [
    "Logging Configuration",
    "",
    "Control console output level and file logging.",
    "",
    "Console Settings:",
    "  - logging.console_level: Minimum log level for console output",
    "    Available levels: DEBUG, INFO, WARNING, ERROR",
    "  - logging.prefix: Text shown before every console line, e.g. 'KitsuneLab | INFO | ...'",
    "    Ships with 'KitsuneLab' as the install-time default, editable here",
    "",
    "File Logging:",
    "  - logging.file_enabled: Enable daily rotating log files (true/false)",
    "  - logging.max_size_mb: Maximum total log directory size in MB",
    "  - logging.max_files: Maximum number of log files to keep",
    "  - logging.max_days: Maximum age of log files in days (365 day hard cap always applies)",
    "  - Yesterday's file and older are gzip-compressed to save space",
    "  - Captures both the egg's own messages and the game server's console output,",
    "    exactly what the panel's console tab shows",
    "",
    "Log files stored in: /home/container/egg/logs/YYYY-MM-DD.log(.gz)",
    "Rotation triggers when ANY limit is reached (size OR count OR age)",
    "",
    "Note: This config is always loaded and does not require an environment variable.",
    "",
    "Config location: /home/container/egg/configs/logging.json"
  ],
  "logging": {
    "console_level": "INFO",
    "prefix": "KitsuneLab",
    "file_enabled": false,
    "max_size_mb": 100,
    "max_files": 30,
    "max_days": 7
  }
}`

const guardTemplate = `{
  "version": "0.0.0",
  "_description": [
    "Bot-Guard Configuration: state-aware connection defense",
    "",
    "Protects against advertising bots that connect, never finish signon, and spray",
    "stray packets / connect floods. Detection is behaviour-based, NOT",
    "name-based: a client that never becomes a fully-joined, Steam-validated player yet",
    "still acts or persists is blocked. See docs/modules/guard.md.",
    "",
    "Why this cannot drop packets: the CS2 transport is encrypted (GameNetworkingSockets)",
    "and the container has no NET_ADMIN, so the guard reads the server log and reacts with",
    "engine console commands (kickid / addip). Every hit is also sent to the node's",
    "Host Guard when one is installed, which turns it into a real packet drop",
    "(docs/modules/guard.md). With the Host Guard installed, action 'log' is enough.",
    "",
    "Default ban tiers: what can never be legitimate bites for 24h (engine-confirmed rcon",
    "bruteforce, malformed packets); behaviour that a broken real client could show for a",
    "moment stays at 30-360 minutes.",
    "",
    "Top-level settings:",
    "  - action: 'log' (dry-run, detect only) | 'kick' | 'block' (kick + temporary ip ban)",
    "  - ban_minutes: default temporary block length in minutes (per-rule override below)",
    "  - cooldown_secs: minimum seconds between two reactions on the same ip",
    "  - stuck_grace_secs: grace period before a client that never reached FULL is suspect",
    "  - whitelist_steamids / whitelist_ips: never actioned (put your admins here)",
    "",
    "IMPORTANT: all blocks are TEMPORARY (self-expiring addip <minutes>). There is no",
    "permanent ban and no persistent denylist on purpose: bots use real accounts on",
    "rotating / CGNAT IPs, so a permanent block would eventually hit an innocent user.",
    "",
    "Each behavior_rule:",
    "  - name: rule id shown in logs",
    "  - type: 'counter' (per client) | 'counter_ip' | 'timeout'",
    "  - match: substring/regex fragment the engine matches against a log line",
    "  - threshold + window_secs: how many hits within how many seconds trip the rule",
    "  - ban_minutes: temporary block length for this rule",
    "  - enabled: false disables the rule without deleting it",
    "  - log: false keeps the rule active but silent: no console line when it fires",
    "",
    "Start on 'log', watch the console for KL-GRD-01 lines, confirm no real player is",
    "flagged, then switch action to 'block'.",
    "",
    "Enable by setting ENABLE_GUARD=1 in the Pterodactyl egg.",
    "",
    "Config location: /home/container/egg/configs/guard.json"
  ],
  "action": "log",
  "ban_minutes": 30,
  "cooldown_secs": 30,
  "stuck_grace_secs": 12,
  "whitelist_steamids": [],
  "whitelist_ips": [],
  "behavior_rules": [
    {
      "name": "reconnect_stuck",
      "type": "counter",
      "match": "Forcing client reconnect",
      "threshold": 3,
      "window_secs": 15,
      "ban_minutes": 360,
      "enabled": true,
      "log": true
    },
    {
      "name": "move_not_joined",
      "type": "counter",
      "match": "failed delta decode, discarding move msg",
      "threshold": 5,
      "window_secs": 15,
      "ban_minutes": 360,
      "enabled": true,
      "log": true
    },
    {
      "name": "stray_no_connection",
      "type": "counter_ip",
      "match": "Stray data packet from host with no connection",
      "threshold": 4,
      "window_secs": 10,
      "ban_minutes": 30,
      "enabled": true,
      "log": true
    },
    {
      "name": "connect_flood",
      "type": "counter_ip",
      "match": "Receiving C2S_CONNECT",
      "threshold": 6,
      "window_secs": 10,
      "ban_minutes": 30,
      "enabled": true,
      "log": true
    },
    {
      "name": "rcon_bruteforce",
      "type": "counter_ip",
      "match": "for rcon hacking attempts",
      "threshold": 1,
      "window_secs": 10,
      "ban_minutes": 1440,
      "enabled": true,
      "log": true
    },
    {
      "name": "malformed_packet",
      "type": "counter_ip",
      "match": "Invalid lead/length byte",
      "threshold": 2,
      "window_secs": 10,
      "ban_minutes": 1440,
      "enabled": true,
      "log": true
    },
    {
      "name": "stuck_no_full",
      "type": "timeout",
      "match": "Sending keepalive",
      "threshold": 3,
      "window_secs": 10,
      "ban_minutes": 60,
      "enabled": true,
      "log": true
    }
  ]
}`
