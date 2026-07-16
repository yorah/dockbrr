package version

// Version is the dockbrr build version.
//
// It is a var (not const) so release builds can override it at link time via
// -ldflags "-X dockbrr/internal/version.Version=<tag>" (see .goreleaser.yaml).
// The literal below is kept in sync with releases by release-please via the
// x-release-please-version annotation.
var Version = "0.3.1" // x-release-please-version
