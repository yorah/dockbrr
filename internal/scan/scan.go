// Package scan is the read-only detection orchestrator: it drives the detector
// and, on a fresh update, enriches it with a changelog. Both the manual `check`
// endpoint and the scheduler call it. It never mutates Docker.
package scan

import (
	"context"
	"encoding/json"
	"sync"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// Detector runs read-only drift detection for one service. *detect.Detector
// satisfies it.
type Detector interface {
	Detect(ctx context.Context, svc store.Service) (*store.Update, error)
}

// Changelog resolves changelog text/url for an update. *changelog.Resolver
// satisfies it.
type Changelog interface {
	Resolve(ctx context.Context, u store.Update, img registry.RemoteImage) (text, url string, err error)
}

// stateInvalidator drops a service's cached remote-resolution so the next detect
// does a full network resolve + semver scan instead of the digest-only
// short-circuit. *store.RemoteStates satisfies it. A manual check uses it so the
// button always re-scans; the periodic poll does not, keeping the cache.
type stateInvalidator interface {
	Invalidate(repo, tag string) error
}

// Scanner ties detection + changelog + persistence together.
type Scanner struct {
	detector  Detector
	changelog Changelog
	services  *store.Services
	updates   *store.Updates
	images    *store.Images
	// states, when non-nil, invalidates a service's detect cache before a manual
	// CheckServiceFresh so the button forces a full re-scan.
	states stateInvalidator
	// notify, when non-nil, is called with the service id whenever CheckService
	// finds a fresh update. It is a plain callback so scan never imports httpapi
	// (avoids an import cycle); only cmd/dockbrr wires it to the event bus.
	notify func(serviceID int64)

	// notifiedTo dedups the notify callback: it maps a service id to the
	// to-digest last notified for it. Detect returns the standing update every
	// poll while drift persists, so without this we'd re-fire the refresh hint
	// each cycle. A cleared/superseded drift deletes the entry so a later,
	// different drift re-notifies. Guarded by mu.
	mu         sync.Mutex
	notifiedTo map[int64]string
}

// New builds a Scanner. notify may be nil; when set it is called with the
// service id on each fresh detection (used to push a "detected" refresh hint).
// states may be nil (disables the manual-check cache invalidation).
func New(detector Detector, cl Changelog, services *store.Services, updates *store.Updates, images *store.Images, states stateInvalidator, notify func(serviceID int64)) *Scanner {
	return &Scanner{detector: detector, changelog: cl, services: services, updates: updates, images: images, states: states, notify: notify, notifiedTo: make(map[int64]string)}
}

// CheckServiceFresh invalidates the service's cached remote-resolution, then runs
// CheckService. The invalidation forces detect past its digest-only cache
// short-circuit so a manual "Check" always does a full network + semver scan
// (and thus re-resolves the changelog). The periodic poll calls CheckService
// directly and keeps the cache for efficiency.
func (s *Scanner) CheckServiceFresh(ctx context.Context, serviceID int64) error {
	if s.states != nil {
		svc, err := s.services.Get(serviceID)
		if err != nil {
			return err
		}
		repo, tag := detect.SplitRef(svc.ImageRef)
		if err := s.states.Invalidate(repo, tag); err != nil {
			logger.Errorf("scan: invalidate detect cache (service %d (%s)): %v", serviceID, svc.Name, err)
		}
		logger.Debugf("scan: manual re-check service %d (%s) (cache invalidated)", serviceID, svc.Name)
	}
	return s.CheckService(ctx, serviceID)
}

// CheckService detects drift for one service and, on a fresh update, resolves +
// persists its changelog. A changelog miss/failure is non-fatal.
func (s *Scanner) CheckService(ctx context.Context, serviceID int64) error {
	svc, err := s.services.Get(serviceID)
	if err != nil {
		return err
	}
	logger.Debugf("scan: checking service %d (%s) ref=%s", svc.ID, svc.Name, svc.ImageRef)
	upd, err := s.detector.Detect(ctx, svc)
	if err != nil {
		return err
	}
	if upd == nil {
		logger.Debugf("scan: service %d (%s) up to date", svc.ID, svc.Name)
		// Drift cleared (or never existed): forget any prior notify so a future,
		// different drift for this service fires the hint again.
		s.mu.Lock()
		delete(s.notifiedTo, serviceID)
		s.mu.Unlock()
		return nil // up-to-date / unmonitorable
	}
	logger.Infof("scan: update available service %d (%s): %s -> %s [%s]",
		svc.ID, svc.Name, refLabel(upd.FromVersion, upd.FromDigest), refLabel(upd.ToVersion, upd.ToDigest), upd.Severity)
	if s.notify != nil && s.markNotified(serviceID, upd.ToDigest) {
		s.notify(serviceID)
	}

	// Reconstruct the image's labels from what the detector already stored, no
	// second registry round-trip.
	var labels map[string]string
	repo, _ := detect.SplitRef(svc.ImageRef)
	if img, gerr := s.images.GetByDigest(repo, upd.ToDigest); gerr == nil && img.Labels != "" {
		_ = json.Unmarshal([]byte(img.Labels), &labels)
	}
	remote := registry.RemoteImage{Ref: svc.ImageRef, Digest: upd.ToDigest, Labels: labels}

	text, url, err := s.changelog.Resolve(ctx, *upd, remote)
	if err != nil {
		logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
		return nil // non-fatal
	}
	if text != "" || url != "" {
		if err := s.updates.SetChangelog(upd.ID, url, text); err != nil {
			logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, err)
		}
	}
	return nil
}

// markNotified records that serviceID's standing drift now targets toDigest and
// reports whether that is a change from the last notified digest. It returns
// false (skip the hint) when the same drift is re-detected on a later poll.
func (s *Scanner) markNotified(serviceID int64, toDigest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notifiedTo[serviceID] == toDigest {
		return false
	}
	s.notifiedTo[serviceID] = toDigest
	return true
}

// CheckAll runs CheckService over every service. Per-service errors are logged,
// never abort the sweep.
func (s *Scanner) CheckAll(ctx context.Context) error {
	svcs, err := s.services.List()
	if err != nil {
		return err
	}
	logger.Infof("scan: checking %d service(s)", len(svcs))
	for _, svc := range svcs {
		if err := s.CheckService(ctx, svc.ID); err != nil {
			logger.Errorf("scan: check service %d (%s): %v", svc.ID, svc.Name, err)
		}
	}
	logger.Debugf("scan: check-all complete (%d service(s))", len(svcs))
	return nil
}

// refLabel prefers a human version, falling back to a short digest, for log
// lines. Empty version + empty digest yields "?".
func refLabel(version, digest string) string {
	if version != "" {
		return version
	}
	if len(digest) > 19 {
		return digest[:19]
	}
	if digest != "" {
		return digest
	}
	return "?"
}
