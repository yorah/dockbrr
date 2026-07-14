package discovery

import "context"

// Locator re-discovers the current container ids + running digest for a single
// service after a compose up recreated its container(s). It reuses Group so the
// matching rules stay identical to reconciliation. Returns primitives only so
// this package never imports the job package (no import cycle).
type Locator struct{ src Collector }

// NewLocator builds a Locator over a container source (*docker.Client).
func NewLocator(src Collector) *Locator { return &Locator{src: src} }

// LocateService returns the container ids and running digest for the named
// service under the named project, or (nil, "", nil) when the service is not
// currently present (caller falls back to the pre-apply ids).
func (l *Locator) LocateService(ctx context.Context, projectName, serviceName string) ([]string, string, error) {
	cs, err := l.src.Collect(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, g := range Group(cs) {
		if g.Name != projectName {
			continue
		}
		for _, s := range g.Services {
			if s.Name == serviceName {
				return s.ContainerIDs, s.CurrentDigest, nil
			}
		}
	}
	return nil, "", nil
}
