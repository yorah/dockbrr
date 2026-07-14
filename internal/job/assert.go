package job

import (
	"dockbrr/internal/discovery"
	"dockbrr/internal/docker"
	"dockbrr/internal/registry"
)

// Compile-time guarantees that the concrete types Phase 6 will wire into the
// engine satisfy the Job Engine interfaces. A signature drift in any of these
// dependencies is caught here at build time rather than when main.go is wired.
var (
	_ Resolver     = (*registry.Resolver)(nil)
	_ Inspector    = (*docker.Client)(nil)
	_ Rediscoverer = (*discovery.Locator)(nil)
	_ Composer     = RealComposer{}
	_ Handler      = (*Applier)(nil)
	_ Emitter      = (*Engine)(nil)
)
