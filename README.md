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

### Docker image

Multi-arch images (amd64/arm64) are published to the GitHub Container Registry
at [`ghcr.io/yorah/dockbrr`](https://github.com/yorah/dockbrr/pkgs/container/dockbrr).

```bash
docker run -d --name dockbrr \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v dockbrr-data:/data \
  -e DOCKBRR_DATA_DIR=/data \
  ghcr.io/yorah/dockbrr:latest
```

Or with Docker Compose, save this as `compose.yaml` and run `docker compose up -d`:

```yaml
services:
  dockbrr:
    image: ghcr.io/yorah/dockbrr:latest
    container_name: dockbrr
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - DOCKBRR_DATA_DIR=/data
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - dockbrr-data:/data

volumes:
  dockbrr-data:
```

The image bundles the Docker CLI + Compose plugin and drives your host's Docker
through the mounted socket. To *apply* updates to a Compose project, the
container also needs to see that project's files at the same paths they live on
the host, so bind-mount those directories too (see the path note below).

### Prebuilt binaries and packages

Each [release](https://github.com/yorah/dockbrr/releases) attaches:

- Standalone binaries (`dockbrr_<version>_<os>_<arch>.tar.gz` / `.zip`) for
  linux, macOS, and Windows on amd64 and arm64. Extract and run `./dockbrr`.
- `.deb` and `.rpm` packages for Debian/Ubuntu and RHEL/Fedora, installing
  `dockbrr` to `/usr/bin`.

### Build from source

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

## Running behind a reverse proxy

dockbrr speaks plain HTTP and expects a reverse proxy to terminate TLS. Two
things matter:

- **Forward `X-Forwarded-Proto`.** dockbrr marks its cookies `Secure` only
  when the request arrived over HTTPS, which it detects from direct TLS or
  the `X-Forwarded-Proto: https` header. Every mainstream proxy sets this by
  default or with one line.
- **Login lockout is per source IP.** After 5 failed logins from one address,
  dockbrr rejects further attempts from it for 15 minutes. dockbrr reads the
  TCP peer address, not `X-Forwarded-For` (that header is spoofable), so
  behind a proxy all clients share the proxy's address and the lockout is
  effectively global. For a single-user tool that's the safe trade.

Caddy example:

```text
dockbrr.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx example:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-Proto $scheme;
    # SSE (live updates): disable buffering so events stream through.
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

## Backup and restore

Everything dockbrr owns lives in the data directory (`--data-dir`, default
`./data`):

- `dockbrr.db` (+ `-wal`/`-shm` sidecars): SQLite database, settings,
  projects, update history, jobs.
- `secret.key`: the encryption key for registry credentials and the GitHub
  token. **Without this file those secrets are unrecoverable**; the rest of
  the database remains readable.
- `logs/`: rotated log files (safe to skip).

To back up: stop dockbrr (or accept a crash-consistent copy, SQLite in WAL
mode tolerates it) and copy the directory. To restore: place the directory
back and start dockbrr; there is no import step. Snapshots taken before
updates live in the database, so restoring the directory restores rollback
history too.

## GitHub token and changelogs

dockbrr fetches changelogs and release notes from the GitHub Releases API. Without
a token those requests are anonymous and GitHub throttles them to 60 per hour, so
on a busy dashboard changelogs start showing "GitHub rate limit reached" instead of
release notes. Setting a token raises the limit to 5000 per hour.

To create one:

1. Go to https://github.com/settings/tokens and choose "Generate new token (classic)".
2. Name it (e.g. `dockbrr-changelog`) and pick an expiry.
3. Leave every scope unchecked. Reading public release notes needs no scopes.
4. Generate the token and copy it.
5. In dockbrr, open Settings, Registries, and paste it into "GitHub token", then Save.

The token is stored write-only and is never shown again. It is used only for
changelog and release-note reads.

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
