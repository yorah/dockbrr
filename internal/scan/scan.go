// Package scan is the read-only detection orchestrator: it drives the detector
// and, on a fresh update, enriches it with a changelog. Both the manual `check`
// endpoint and the scheduler call it. It never mutates Docker.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"dockbrr/internal/changelog"
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
	// A manual check is the explicit "look again" gesture, so it also lifts the
	// rolled_back suppression (RecordDrift preserves rolled_back on scheduled
	// polls so auto-apply can never re-apply a just-reverted target on its own).
	if s.updates != nil {
		if n, err := s.updates.ReopenRolledBack(serviceID); err != nil {
			logger.Errorf("scan: reopen rolled-back updates (service %d): %v", serviceID, err)
		} else if n > 0 {
			logger.Infof("scan: service %d manual check reopened %d rolled-back update(s)", serviceID, n)
		}
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
		// A historyless, up-to-date service still deserves a changelog: write a
		// synthetic current-version row (from == to) so the dashboard can show
		// what the running version shipped. Only when the service has no update
		// row at all, so we never shadow real (open/applied/dismissed) history.
		if err := s.ensureCurrentChangelog(ctx, svc); err != nil {
			logger.Errorf("scan: ensure current changelog (service %d (%s)): %v", svc.ID, svc.Name, err)
		}
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
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(upd.ID, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (update %d): %v", upd.ID, serr)
		}
	case err != nil:
		logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(upd.ID, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, serr)
		}
	}
	return nil
}

// ensureCurrentChangelog writes a synthetic status='current' update row for an
// up-to-date, historyless service and resolves its changelog. The row is
// from == to == the current running version, so the resolver returns that
// version's own release notes. It is inert everywhere pending/applied logic
// lives (those key on 'available'/'applied'); only the dashboard's last-applied
// fallback surfaces it. A missing/failed changelog is non-fatal.
func (s *Scanner) ensureCurrentChangelog(ctx context.Context, svc store.Service) error {
	if svc.CurrentDigest == "" {
		return nil // nothing to key the row on
	}
	has, err := s.updates.HasAnyByService(svc.ID)
	if err != nil {
		return err
	}
	if has {
		return nil // real (or prior current) history already provides a changelog
	}

	repo, tag := detect.SplitRef(svc.ImageRef)
	version := svc.ImageVersion
	var labels map[string]string
	if img, gerr := s.images.GetByDigest(repo, svc.CurrentDigest); gerr == nil {
		if img.ResolvedVersion != "" {
			version = img.ResolvedVersion
		}
		if img.Labels != "" {
			_ = json.Unmarshal([]byte(img.Labels), &labels)
		}
	}
	if version == "" {
		version = tag
	}

	row := store.Update{
		ServiceID:   svc.ID,
		FromDigest:  svc.CurrentDigest,
		ToDigest:    svc.CurrentDigest,
		FromVersion: version,
		ToVersion:   version,
		Tag:         tag,
		Severity:    "current",
		Status:      "current",
	}
	id, err := s.updates.Upsert(row)
	if err != nil {
		return err
	}
	row.ID = id

	remote := registry.RemoteImage{Ref: svc.ImageRef, Digest: svc.CurrentDigest, Labels: labels}
	text, url, err := s.changelog.Resolve(ctx, row, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(id, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (current row %d): %v", id, serr)
		}
	case err != nil:
		return err
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(id, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (current row %d): %v", id, serr)
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
