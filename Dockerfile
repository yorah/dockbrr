# dockbrr ships as a single static binary, so the release image only needs to
# provide the runtime dependencies dockbrr shells out to. dockbrr drives the
# host Docker daemon over the mounted socket and execs `docker compose ...`, so
# the image needs the Docker CLI + compose plugin (bundled in docker:cli) --
# distroless/scratch would break compose operations.
#
# The binary is built ahead of time by GoReleaser (SPA embedded via embed.FS)
# and simply copied in here. Run with the host socket mounted, e.g.:
#   docker run -v /var/run/docker.sock:/var/run/docker.sock -p 8080:8080 \
#     -v dockbrr-data:/data ghcr.io/yorah/dockbrr
FROM docker:28-cli

# GoReleaser (dockers_v2) lays the per-platform binary out under $TARGETPLATFORM/
# (e.g. linux/amd64/dockbrr, linux/arm64/dockbrr) in the build context.
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/dockbrr /usr/local/bin/dockbrr

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dockbrr"]
