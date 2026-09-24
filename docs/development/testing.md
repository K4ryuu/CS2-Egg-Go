# Testing

```bash
make test           # everything
go test ./tests/unit/node/guard/ -v
go test -run Workshop ./tests/...
```

Tests live under `tests/`, never next to the source, and the tree mirrors the packages they cover. Every test file is an external package (`package guard_test`), so it can only use what the package exports. A test that needs an unexported thing is telling you the seam is in the wrong place.

```
tests/
  unit/            one directory per package
    egg/...        configs, console, guard, addons, startup, steamcmd, watch
    node/...       core, guard, vpksync, workshop, addoncache, alerts, backup, cleanup, crashes, metrics, ui, update, install
    cleanup/       the file cleanup engine both sides run
    node/volume/   proves nothing the daemon writes can escape a server volume
    proto, logx, netx, config, version
  integration/
    supervisor/    a real pty, a real child process, echo and shutdown
    nodelink/      the real egg boot against a fake node on a real socket
  manual/          a renderer for eyeballing the top layout, off by default
```

## What the suites are for

**Unit tests carry the old bash suites.** The behaviour the shell version got right over two years is encoded here: the LilithBot idle case at 64 packets a second, the reconnect race, rcon from a trusted address still being blocked, `Console (0)` chat being ignored, the escalation ladder, the address classification matrix including `100.64.0.1` counting as public.

**Integration tests run the real thing.** `nodelink` boots `boot.Run` with a fake node listening on a real unix socket and a fake CS2 printing real console lines. It proves the egg reports its state after the hello, restates after the node restarts, and sends a crash with the console tail. That suite caught two bugs that unit tests could not see.

**The volume suite is a security test.** It plants symlinks pointing out of a server volume and asserts that the push, the addon delivery, the restore, the backup and the crash bundler all refuse to follow them.

## Writing one

Test through the exported interface, at a seam that was agreed before the code was written. Assert behaviour, not structure: a refactor inside a package should not touch a test file.

Bug fixes get a test that fails before the fix. Not for ceremony: a bug that came back once will come back again.

Skip tests for trivial getters and one-line helpers. The suite is fast because it does not test things that cannot break.

## Platform notes

A few tests are Linux-only (mount namespaces, nftables) and build-tagged. On macOS they are skipped, and the rest of the suite runs. Unix socket paths are capped near 104 bytes, so tests that need a socket use `/tmp` rather than the default temp directory.

---

[Docs index](../README.md)
