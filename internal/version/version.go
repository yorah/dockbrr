package version

// Version is the dockbrr build version.
//
// It is a var (not const) so release builds can override it at link time via
// -ldflags "-X dockbrr/internal/version.Version=<tag>" (see .goreleaser.yaml).
// The literal below is kept in sync with releases by release-please via the
// x-release-please-version annotation.
var Version = "0.12.2" // x-release-please-version

// Commit, CommitDirty and BuildDate are stamped at link time via -ldflags -X
// (see mise.toml build task and .goreleaser.yaml). They are the authoritative
// source for the build metadata shown in Settings → Application.
//
// Why not Go's own vcs.* build info? Both build paths run `npm run build`, which
// overwrites the git-tracked dist/index.html placeholder before `go build` runs.
// That leaves the working tree dirty at compile time, so Go's auto-stamped
// vcs.modified would always be "true" for a clean release. These vars are
// computed BEFORE the SPA clobbers the tree (mise), or from goreleaser's own
// git metadata (release), so they stay honest. When empty (plain `go build` /
// `go run`), buildStamps falls back to Go's vcs.* build info.
var (
	Commit      = ""
	CommitDirty = ""
	BuildDate   = ""
)
