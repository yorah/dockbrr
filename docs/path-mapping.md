# Path mapping: letting dockbrr reach your compose files

When dockbrr runs inside a container, it can always *see* your projects
(discovery reads container labels through the Docker socket), but to *apply*
updates to a Compose project, or show its compose file in the UI, it must be
able to open that project's files **at the same absolute path they have on the
host**.

If it can't, the compose viewer shows an error like:

```
open /home/you/myproject/compose.yml: no such file or directory
```

and applying updates to that project is refused.

## Why the paths must match

dockbrr applies updates by exec'ing the real `docker compose` CLI against your
host's Docker daemon (through the mounted socket). Two things follow:

1. dockbrr's container needs to read the compose file itself, so the file must
   exist inside the container.
2. `docker compose` resolves relative bind mounts and `env_file`s against the
   project directory, and sends those paths to the **host** daemon. If the
   project lived at a different path inside dockbrr's container, the daemon
   would create host binds pointing at the wrong (host) location.

Mounting the project directory at its identical host path satisfies both at
once. There is nothing to configure in dockbrr itself.

## docker run

Add one `-v` per project directory (same path on both sides), next to the
socket mount:

```bash
docker run -d --name dockbrr \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v dockbrr-data:/data \
  -e DOCKBRR_DATA_DIR=/data \
  -v /home/you/myproject:/home/you/myproject \
  ghcr.io/yorah/dockbrr:latest
```

A parent directory that contains all your compose projects works too, and
covers future projects without another mount:

```bash
  -v /srv/stacks:/srv/stacks
```

## Docker Compose

Same idea in the `volumes` list of dockbrr's own service:

```yaml
services:
  dockbrr:
    image: ghcr.io/yorah/dockbrr:latest
    ports:
      - "8080:8080"
    environment:
      - DOCKBRR_DATA_DIR=/data
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - dockbrr-data:/data
      - /home/you/myproject:/home/you/myproject
      # or a parent dir covering several projects:
      # - /srv/stacks:/srv/stacks

volumes:
  dockbrr-data:
```

Restart dockbrr after adding mounts (`docker compose up -d` recreates it).

## Notes

- Mount read-write, not `:ro`, if you use the "write back compose file"
  setting (dockbrr then pins the new version into the file after an apply).
  Without write-back, `:ro` is fine.
- **Standalone containers** (not managed by a compose project) don't need any
  of this: dockbrr updates them through the Docker API directly, no files
  involved.
- Running the dockbrr binary directly on the host (no container) also needs
  none of this; it reads the files where they are.
