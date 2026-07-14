# dockbrr logging: design spec

**Date:** 2026-07-12
**Status:** approved (approach), pending spec review

## Problem

dockbrr logs only `log.Printf` to stderr. On launch the operator sees a single
`dockbrr 0.1.0-dev listening on :3625` line and nothing else useful. There is no
level control, no persistent file, no rotation, and no way to view/download logs
from the UI. Diagnosing a bad apply or a stuck scan means nothing is captured.

## Goal

Structured, leveled logging with UI visibility:

- Leveled logging: `trace / debug / info / warn / error`.
- Rotating log **file** alongside console output, configured by **path / level /
  max size / max backups**.
- A **Logs** settings tab showing that config plus a **table of log files**
  (name / last modified / size / download icon).

## Decisions (locked)

1. **Level is live-editable from the UI**: stored as a DB setting, applied
   instantly via zerolog's atomic global level (no restart). Path / max-size /
   max-backups stay bootstrap-only, since they must be known before the logger
   and the DB exist.
2. **Full migration**: every non-test `log.*` call site (37 across 11 files)
   converted to a leveled call.
3. **Log endpoints are session-gated**: same auth middleware as the rest of
   `/api`.

## Library choice

**`github.com/rs/zerolog` + `gopkg.in/natefinch/lumberjack.v2`.**

- zerolog has all five requested levels (stdlib `slog` has no Trace); a
  familiar, well-worn config shape.
- lumberjack gives size-based rotation with `MaxBackups`; both are pure-Go, so
  the `CGO_ENABLED=0` single-static-binary invariant holds. (Both already appear
  transitively: `sirupsen/logrus` pulls neither; these become direct deps.)

## Components

### `internal/logger` (new)

Owns logger init, live level control, and log-file enumeration. Uses zerolog's
**global** logger so call sites need no dependency injection (they already used
the global stdlib `log`).

```go
package logger

type Config struct {
    Path       string // "" => console only, no file
    Level      string // initial level: trace|debug|info|warn|error
    MaxSizeMB  int
    MaxBackups int
}

// Init configures the global zerolog logger: a console writer (stderr,
// human-readable) plus, when Path != "", a lumberjack file writer. It also
// redirects the stdlib `log` package into zerolog so any stray/third-party
// log.Print still lands in the file. Returns the resolved absolute log dir.
func Init(cfg Config) (dir string, err error)

// SetLevel swaps the global level atomically (zerolog.SetGlobalLevel). Invalid
// level => error, level unchanged.
func SetLevel(level string) error

// ParseLevel validates/normalises a level string.
func ParseLevel(s string) (zerolog.Level, error)

// Printf-style helpers keep the migration 1:1 with existing call sites:
func Tracef(format string, a ...any)
func Debugf(format string, a ...any)
func Infof(format string, a ...any)
func Warnf(format string, a ...any)
func Errorf(format string, a ...any)

// FileInfo is one row of the UI table.
type FileInfo struct {
    Name     string    `json:"name"`
    Modified time.Time `json:"modified"`
    Size     int64     `json:"size"`
}

// Files lists the log directory (the active file + lumberjack's rotated
// `dockbrr-<timestamp>.log[.gz]` siblings), newest first. Empty when Path == "".
func Files() ([]FileInfo, error)

// Open opens a named log file for download. name must be a base name resolving
// inside the log dir; anything else => error (path-traversal guard).
func Open(name string) (io.ReadCloser, error)
```

Level assignment for the migration (informational → Info, degraded-but-continuing
→ Warn, real failure → Error):

| Site pattern | Level |
|---|---|
| "listening on", "bootstrapped admin", "re-queued N interrupted", "poll interval now" | Info |
| "docker unreachable …", "job engine idle + discovery disabled", "(skipped)", "(semver scan skipped)", "(labels skipped)", "no handler set" | Warn |
| "reconcile error", "check-all", "resolve %v", "credential lookup/decrypt", "cache get/put", "handler panic", "append log", "claim-next", "event stream ended", `httpapi: %s: %v` | Error |
| `main` fatal (`run` returned) | Error then `os.Exit(1)` |

### `internal/config` (extend)

Four new bootstrap fields + flags/env, resolved before `store.Open`:

| flag | env | default |
|---|---|---|
| `--log-path` | `DOCKBRR_LOG_PATH` | `<data-dir>/logs/dockbrr.log` |
| `--log-level` | `DOCKBRR_LOG_LEVEL` | `info` |
| `--log-max-size` (MB) | `DOCKBRR_LOG_MAX_SIZE` | `50` |
| `--log-max-backups` | `DOCKBRR_LOG_MAX_BACKUPS` | `3` |

`cmd/dockbrr/main.go` calls `logger.Init` immediately after `config.Load` +
`os.MkdirAll(logDir)`, **before** opening the DB, so DB-open failures are logged
to the file. After the store is up, it reads the `log_level` DB setting (if
present) and calls `logger.SetLevel` so the persisted UI choice wins over the
bootstrap default.

### `internal/httpapi/logs.go` (new)

Registered on the authed router in `server.go`:

- `GET /api/logs/config` → `{path, level, maxSizeMB, maxBackups}`. `level` is the
  effective level (DB setting or bootstrap default). For display.
- `GET /api/logs/files` → `[]FileInfo`.
- `GET /api/logs/files/{name}/download` → streams the file
  (`Content-Disposition: attachment`). Uses `logger.Open`: base-name only,
  must resolve inside the log dir, else `400`. `.gz` served as-is.

Level changes reuse the **existing** settings PUT path: writing `log_level`
persists it and the handler calls `logger.SetLevel`. No new mutation endpoint.

`Deps` gains `LogConfig` (path/maxSize/maxBackups: the static bits) so the
config endpoint can report them without re-reading flags.

### Frontend: `web/src/components/settings/LogsSettings.tsx` (new) + tab

New **Logs** tab in `settings.tsx`. Contents:

- **Config card**: path (read-only), max size, max backups (read-only), and a
  **level `<select>`** (trace→error) wired to a TanStack mutation that PUTs
  `log_level`. Invalidates the config query on success.
- **Files table** (existing table primitives): columns **Name · Last modified ·
  Size · Download**. Download is an anchor to
  `/api/logs/files/{name}/download` (credentials-included, same-origin: no CSRF
  needed for GET). Size humanised, modified as locale datetime. Empty state when
  no files / file logging disabled.

## Data flow

```
config.Load ──► logger.Init(path,level,maxsize,maxbackups)
                   │  console writer + lumberjack file writer
                   │  stdlib log redirected ──► zerolog
store.Open ──► settings.Get("log_level") ──► logger.SetLevel   (persisted override)

UI level change ──► PUT settings {log_level} ──► settings handler
                       ├─ persist DB setting
                       └─ logger.SetLevel   (atomic, live)

UI Logs tab ──► GET /api/logs/config   (static + effective level)
            ──► GET /api/logs/files    (logger.Files)
            ──► GET /api/logs/files/{name}/download  (logger.Open, traversal-guarded)
```

## Error handling

- `logger.Init` failure (can't create/open file) is fatal at boot, logging is
  infra; fail loud rather than silently drop the file.
- `SetLevel` with a bad value returns an error; the settings PUT surfaces `400`
  and the level is unchanged.
- `Open`/download rejects non-base names, names with separators, or paths that
  escape the log dir → `400`; missing file → `404`.
- `Files` on a missing/empty dir returns `[]`, not an error.

## Testing

- **logger**: Init writes to the file; level gate drops sub-level lines; SetLevel
  toggles live; `Files` lists + sorts newest-first; `Open` rejects `../etc`,
  absolute paths, and names with slashes, accepts a real base name.
- **config**: new flags/env parse with precedence flag > env > default.
- **httpapi/logs**: list returns rows; download streams bytes + attachment
  header; traversal → 400; unauthenticated → 401 (auth-gate).
- **frontend** (vitest): LogsSettings renders the table from a mocked
  `/api/logs/files`; level select fires the PUT mutation; download anchor href.

## Non-goals

- No in-browser log tailing / live streaming (files + download only).
- No per-module log levels.
- No JSON-vs-console toggle. Both writers use zerolog's console format (no
  colour on the file) so downloaded logs are human-readable; revisit if
  structured/parseable file output is later needed.
- No retention beyond lumberjack's `MaxBackups` + its built-in age/size limits.

## Invariant check

- CGO-free: zerolog + lumberjack are pure Go. ✅
- Docker mutation still only via Job Engine, logging touches nothing there. ✅
- Log download is read-only, auth-gated, path-traversal-guarded. ✅
- No CDN / dangerouslySetInnerHTML added on the frontend. ✅
