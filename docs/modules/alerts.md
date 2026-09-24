# Alerts

A Discord webhook for the things you want to know about without watching a terminal.

## Setup

Create a webhook in Discord: Server Settings > Integrations > Webhooks > New Webhook > Copy URL. Paste it into the wizard. With `--yes` and no webhook the module stays off rather than failing.

The posts carry their own name and picture, so they say what they are instead of whatever the webhook was called when someone made it. `username` defaults to `cs2node` and `avatar_url` takes an https link to a png or jpg; leave it empty to keep the webhook's own picture.

## Events

Pick the ones you want; `all` is the default.

| Event | Fires when |
|---|---|
| `crash` | A server crashed: exit code, map, players, and where the bundle is |
| `cs2.update` | The central CS2 install moved to a new build |
| `push.failed` | A VPK push into a volume failed |
| `restart.deferred` | A post-update restart is waiting for an empty server or a window |
| `guard.block` | The guard dropped an address: ip, minutes, rule, why |
| `node.update` | cs2node updated itself |
| `backup.done`, `backup.failed` | A backup finished or failed |
| `cleanup.done` | A cleanup pass removed files from a volume: how many and how much |
| `server.start`, `server.stop` | A container came up or went away. The stop says why: stopped cleanly, restarting for a CS2 update, killed, or it exited on a crash, with the exit code and the signal behind it. Noisy on a busy node |

## What a message looks like

The facts sit in labelled fields rather than a sentence: an address block shows the address, how long it is blocked for, the rule that caught it, which offence it is for that address, and whether the server console or the traffic shape saw it. A ban reads as `24 hours`, not `1440 min`.

Every message says which server it is about: the name players see sits above the title, and the node plus the short server id in the footer. The short id is what every `cs2node` command takes, so the message is enough to act on.

A stop says why it stopped. CS2 exits on a segfault on every clean shutdown, so the exit code cannot be the thing that decides: the egg knows whether it asked the server to quit and reports a crash only when it did not, and a restart the node itself asked for is known outright. The exit code is shown only when it is part of the answer.

Notices that arrive within three seconds go out as one message, so a CS2 update with restarts on eight servers is one post, not eight. Discord's rate limit is honoured and the post retried once.

`cs2node doctor` checks the webhook without posting anything, and `cs2node status` counts what was sent and what failed.

---

[Docs index](../README.md)
