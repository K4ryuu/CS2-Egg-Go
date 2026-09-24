# Contributing

Bug reports, ideas and pull requests are all welcome. This is a small project with one maintainer, so a short conversation before a big change saves everyone time.

## Reporting a bug

Use the [issue templates](https://github.com/K4ryuu/CS2-Egg-Go/issues/new/choose). What actually helps:

- The server console around the problem, not just the last line.
- `sudo cs2node doctor` output when the node is involved.
- `journalctl -u cs2node -n 100` for anything daemon-side.
- `cs2node version` and the image tag the server runs.

"It does not work" with no output means someone has to ask for all of that anyway.

## Suggesting a feature

Say what you are trying to do, not just what to build. Half the modules in here started as somebody describing a problem, and the answer ended up different from the request.

Something a plugin can do belongs in a plugin. This project is for what needs the node or the container.

## Pull requests

```bash
make test        # go test ./...
make vet         # go vet ./...
```

Both have to pass. CI runs them anyway.

What gets a PR merged quickly:

- One thing at a time. A bug fix and a refactor in the same diff is two reviews.
- A test that fails before your fix. Bugs that came back once come back again.
- The existing style. Look at the file you are editing; it is consistent on purpose.

### The house rules

**Console output is a stable interface.** Server owners read it every day and scripts grep it. The format, the levels, the `KL-` codes: those change only deliberately.

**Modules never import each other.** A node module talks to `core` and nothing else. If two modules need to share a fact, that is what the notice bus is for.

**Tests live in `tests/`, never next to the source**, as external packages. If a test needs something unexported, the seam is in the wrong place.

**Nothing from a container is trusted.** Every write into a server volume goes through a path-restricted root, every message is bounded, every path is validated. See the [security model](../docs/security.md) before touching anything on that boundary.

**No shell.** The image has no shell scripts and the daemon does not spawn one. SteamCMD is invoked with an argument list.

### Comments

Comment why, not what. Exported functions get a doc comment saying what a caller needs to know, including the surprising parts. Skip comments that restate the line under them.

## Where things are

[Architecture](../docs/development/architecture.md) has the map, [building](../docs/development/building.md) has the commands, [testing](../docs/development/testing.md) has the layout of the suites.

## Licence

By contributing you agree your work ships under the repository's [licence](../LICENSE), the GNU General Public License v3 or later. That means anyone who distributes a modified `cs2egg` or `cs2node`, or an image built from them, has to hand over the source of their changes on the same terms.
