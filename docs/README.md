# Documentation

## Start here

| I want to | Read |
|---|---|
| Run a CS2 server on this egg | [install.md](install.md) |
| Share files, block bots, cache workshop content across my servers | [node.md](node.md) |
| Move off the bash egg, or back to it | [migrating-from-bash.md](migrating-from-bash.md) |
| Work out why something is broken | [troubleshooting.md](troubleshooting.md) |

## Guides

| Page | What is in it |
|---|---|
| [install.md](install.md) | Import the egg, first server, what to switch on, reading the console |
| [node.md](node.md) | Install `cs2node`, pick modules, check it, roll it out to a production node |
| [addons.md](addons.md) | Metamod, CounterStrikeSharp, SwiftlyS2, ModSharp: how they install and update |
| [upgrading.md](upgrading.md) | Image tags, release channels, self-update, rollback |
| [migrating-from-bash.md](migrating-from-bash.md) | From the bash egg, and the way back |
| [troubleshooting.md](troubleshooting.md) | Symptom first, then the fix |
| [security.md](security.md) | What the daemon trusts and what it refuses to do |

## Modules

The node daemon is a core plus modules you pick at install. [How they work](modules/README.md), then one page each:

| Module | Gives you |
|---|---|
| [vpk-sync](modules/vpk-sync.md) | One CS2 install per node instead of one per server |
| [guard](modules/guard.md) | Bot floods dropped before they reach a container |
| [workshop](modules/workshop.md) | Maps, models and skins stored once per node |
| [addon-cache](modules/addon-cache.md) | One GitHub fetch per node, plus version locks |
| [alerts](modules/alerts.md) | Crashes and updates in a Discord channel |
| [crashes](modules/crashes.md) | The console tail and dumps of every crash |
| [backups](modules/backups.md) | A nightly archive outside the volumes |
| [metrics](modules/metrics.md) | `cs2node top` and Prometheus |
| [cleanup](modules/cleanup.md) | Demos, logs and dumps deleted on a schedule, fleet-wide |
| [console-log](modules/console-log.md) | The console tab's output, saved past a restart |

## Reference

Look things up here.

| Page | What is in it |
|---|---|
| [panel-variables](reference/panel-variables.md) | Every switch the panel shows |
| [server-configs](reference/server-configs.md) | `egg/configs/*.json`: filter, cleanup, logging, guard |
| [config-vs-variables-audit](reference/config-vs-variables-audit.md) | Every config field, and why it is (or is not) also a panel Variable |
| [node-config](reference/node-config.md) | `/etc/cs2node/config.json`, every key of every module |
| [cli](reference/cli.md) | Every `cs2node` command |
| [error-codes](reference/error-codes.md) | `KL-...` messages, cause and fix |
| [protocol](reference/protocol.md) | What the egg and the node say to each other |

## Development

| Page | What is in it |
|---|---|
| [architecture](development/architecture.md) | The shape of the code, with diagrams |
| [building](development/building.md) | Image, binaries, releases |
| [testing](development/testing.md) | How the suites are laid out and what they cover |

Contributing: [CONTRIBUTING.md](../.github/CONTRIBUTING.md).
