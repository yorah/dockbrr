<img src="docs/brand/mark.svg" width="72" alt="">

# dockbrr

dockbrr is a self-hosted update manager for Docker. It watches your Docker Compose
projects (and standalone containers) for new image versions, shows you what changed,
and lets you apply the update with one click. It never touches Docker on its own
unless you explicitly turn on auto-update for a project.

Single static Go binary with an embedded React web UI. No external database, no
message broker, nothing else to run alongside it.

## What it does

- Watches running containers and Compose projects on a Docker host and groups them
  automatically using the labels Compose already sets.
- Checks registries for newer image digests and tags (Docker Hub, GHCR, and other
  OCI-compatible registries), including multi-arch images.
- Works out a version delta and severity (major/minor/patch/digest-only) when it can
  parse a semver tag, and falls back to "a new digest exists" when it can't.
- Pulls a changelog or release notes for the update where one is available, from OCI
  labels, GitHub releases, or a repo's CHANGELOG file.
- Applies an update through a real `docker compose pull` + `docker compose up`,
  never through the Docker API directly, so the result is identical to running the
  commands yourself.
- Snapshots the previous state before every mutation, health-checks the recreated
  containers, and offers one-click rollback if the health check fails.
- Optionally auto-applies updates on a schedule, per project and per service, off by
  default.

## Safety model

This is the part that matters most, since dockbrr is something you give access to
your Docker socket.

- The UI and API never mutate Docker directly. Every action becomes a job, and only
  the job engine touches Docker.
- A snapshot is taken before every mutation. Rollback always targets that snapshot.
- Every apply pulls first, then brings the stack up. If the pull fails, nothing
  about the running stack changes.
- After bringing a stack up, dockbrr polls the newly recreated containers (not the
  old ones) until they're healthy, or marks the apply failed and offers rollback.
- Only one job can run per project at a time.
- Compose commands are whitelisted and run as direct arguments to `docker compose`,
  never through a shell, so there's no way for anything to inject an arbitrary
  command.

## Getting started

### Requirements

dockbrr applies updates by running `docker compose` on the host, so wherever it
runs needs:

- **Docker Engine** with the **Compose v2 plugin** (the `docker compose`
  subcommand, not the legacy standalone `docker-compose`). It ships with Docker
  Desktop and with most distro Docker packages (`docker-ce`, `docker.io`, or
  `moby-engine`, depending on your distro).
- Access to the Docker socket, `/var/run/docker.sock` by default.

To build from source you'll also need [mise](https://mise.jdx.dev/) (or Go 1.26
and Node.js, if you'd rather run the underlying commands by hand).

### Build from source

dockbrr doesn't have packaged releases or a container image yet. For now, build it
from source.

```bash
git clone https://github.com/yorah/dockbrr.git
cd dockbrr
mise run build   # builds the web UI, then the Go binary, into ./dockbrr
./dockbrr
```

On first run, dockbrr starts a setup wizard in the browser to create your admin
account. Everything else (registries, intervals, auto-update, and so on) is
configured from the UI, so you shouldn't need to touch flags or environment
variables beyond the bootstrap options below.

By default it listens on `:8080` and looks for the Docker socket at
`/var/run/docker.sock`. If dockbrr is running somewhere that can't reach your
Compose project directories at the same paths they exist on the host (for
example, inside a container without matching bind mounts), it can still see
and check those projects, but won't be able to apply updates to them.

## Configuration

Almost everything is configured through the UI after setup. The exceptions are the
handful of values needed before a database exists:

| Flag | Environment variable | Default | What it's for |
| --- | --- | --- | --- |
| `--data-dir` | `DOCKBRR_DATA_DIR` | `./data` | Where the SQLite database and secret key live |
| `--bind` | `DOCKBRR_BIND` | `:8080` | HTTP listen address |
| `--docker-socket` | `DOCKBRR_DOCKER_SOCKET` | `/var/run/docker.sock` | Path to the Docker socket |
| `--admin-user` | `DOCKBRR_ADMIN_USER` | (none) | Bootstraps an admin user on first run instead of using the setup wizard |
| `--admin-password` | `DOCKBRR_ADMIN_PASSWORD` | (none) | Password for the bootstrapped admin user |
| `--log-path` | `DOCKBRR_LOG_PATH` | `<data-dir>/logs/dockbrr.log` | Log file path |
| `--log-level` | `DOCKBRR_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, or `error` |
| `--log-max-size` | `DOCKBRR_LOG_MAX_SIZE` | `50` | Log file rotation size, in MB |
| `--log-max-backups` | `DOCKBRR_LOG_MAX_BACKUPS` | `3` | Number of rotated log files to keep |

## Development

```bash
mise tasks       # list all available tasks
mise run dev      # runs the API on :3625 and the Vite dev server on :5173 together
mise run check    # go vet + go test + the web test suite
mise run build    # production build (what "Getting started" above uses)
```

More detail on the architecture and the pieces that make it up is in `CLAUDE.md`,
and the original design spec is under `docs/dev/specs/`.

## License

GPL-3.0. See [LICENSE](LICENSE).
