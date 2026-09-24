# Upgrading

Two things update on their own schedules: the image your servers run, and the daemon on the node.

## The image

Wings pulls the image when a server starts. So a server picks up a new image on its next full stop and start. The Restart button does not always replace the container; stop and start does.

| Tag | Gets |
|---|---|
| `:stable` | Stable releases |
| `:beta` | What is going into the next stable |

Pinning to a specific version is not offered on purpose: the egg and the daemon speak a versioned protocol, and staying current is how they stay compatible. If you need to hold a server back, hold its addon versions instead with [pins](modules/addon-cache.md#version-locks).

To force a pull on the node:

```bash
sudo docker pull ghcr.io/k4ryuu/cs2-egg-go:stable
```

Then stop and start the server.

## The daemon

```bash
sudo cs2node update      # check now
```

Otherwise it checks once a day and installs what is newer on its channel:

| Channel | Installs |
|---|---|
| `stable` | Stable releases |
| `beta` | Prereleases too |
| `dev` | Nothing. You copy the binary yourself |

A release is the binary plus `checksums.txt`. The daemon downloads both **from GitHub only**, over TLS, with no mirror in between, verifies the SHA-256, swaps the binary atomically and restarts itself through systemd. The previous binary is kept.

A release asset that points anywhere but `github.com` or its own asset hosts is refused before anything is fetched, and a redirect that would leave those hosts is refused too. Nothing inside a release can change that: the checksum is the whole policy, and there are no flags in the payload to honour.

Turn the automatic check off with `"update": {"auto": false}` in the config, or by choosing the `dev` channel.

### Rolling back

```bash
sudo mv /usr/local/bin/cs2node.prev /usr/local/bin/cs2node
sudo systemctl restart cs2node
```

Blocks survive it: they live in the kernel, not in the process.

## Config across versions

`/etc/cs2node/config.json` carries a `version`. A newer daemon migrates it in memory on load, so an old file keeps working and you never rewrite it by hand. New settings get their defaults; yours are kept.

The server-side configs under `egg/configs/` work the same way, per server, on boot. Rules you edited stay, new template rules are appended, removed ones disappear.

## Checking what is running

```bash
cs2node version           # the binary you just ran
cs2node status            # the daemon's own version, and each server's egg build
cs2node doctor            # says so loudly if they disagree
```

`doctor` compares the running daemon against the binary on disk, and each server's egg build stamp against what it should be. That is how you catch a server that never picked up the new image.

---

[Docs index](README.md)
