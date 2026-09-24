# Security policy

## Reporting

Do not open a public issue for a vulnerability. Use GitHub's [private reporting form](https://github.com/K4ryuu/CS2-Egg-Go/security/advisories/new), or reach me on [Discord](https://dsc.gg/k4-fanbase) and ask for a private channel.

Include what you would want to receive: what you did, what happened, and what an attacker gets out of it. A proof of concept helps, even a rough one.

You will get a first reply within a few days. If a fix is needed, you will hear about the timeline and get credit in the release notes unless you would rather not.

## What counts

The interesting boundary is between a server and its node. The daemon runs as root; every server runs as an unprivileged user who can execute arbitrary code inside their own container and write anything into their own volume, including the socket the daemon listens on.

So these are the reports worth sending:

- Anything a container user can do that writes or reads outside their own volume.
- Anything one server can do to another server's files, traffic or configuration.
- Anything that gets code or a chosen file onto the node as root.
- A way to make the daemon fetch from a host it should never fetch from.
- A way to bypass the checksum verification on a self-update.

The [security model](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/security.md) describes what the daemon already assumes and guards against. If you found a way around one of those guarantees, that is exactly the report to send.

## What does not count

- A server operator changing their own server's configuration. That is their server.
- Blocking their own players with a rule they configured.
- Resource use inside their own container.
- Anything that needs root on the node to begin with.

## Supported versions

The current release on the `stable` channel, and whatever is on `beta`. There are no long term support branches: this is one binary with a self-updater, and staying current is the plan.
