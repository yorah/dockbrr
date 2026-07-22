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

// Scanner ties detection + changelog + persistence together.
type Scanner struct {
	detector  Detector
	changelog Changelog
	services  *store.Services
	updates   *store.Updates
	images    *store.Images
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
func New(detector Detector, cl Changelog, services *store.Services, updates *store.Updates, images *store.Images, notify func(serviceID int64)) *Scanner {
	return &Scanner{detector: detector, changelog: cl, services: services, updates: updates, images: images, notify: notify, notifiedTo: make(map[int64]string)}
}

// CheckServiceFresh runs CheckService, first lifting the rolled_back
// suppression: a manual check is the explicit "look again" gesture (RecordDrift
// preserves rolled_back on scheduled polls so auto-apply can never re-apply a
// just-reverted target on its own).
func (s *Scanner) CheckServiceFresh(ctx context.Context, serviceID int64) error {
	_, err := s.checkServiceFresh(ctx, serviceID)
	return err
}

// checkServiceFresh is CheckServiceFresh's internal form, additionally reporting
// whether a changelog resolve hit the GitHub rate limit.
func (s *Scanner) checkServiceFresh(ctx context.Context, serviceID int64) (rateLimited bool, err error) {
	if s.updates != nil {
		if n, rerr := s.updates.ReopenRolledBack(serviceID); rerr != nil {
			logger.Errorf("scan: reopen rolled-back updates (service %d): %v", serviceID, rerr)
		} else if n > 0 {
			logger.Infof("scan: service %d manual check reopened %d rolled-back update(s)", serviceID, n)
		}
	}
	return s.checkService(ctx, serviceID)
}

// CheckService detects drift for one service and, on a fresh update, resolves +
// persists its changelog. A changelog miss/failure is non-fatal.
func (s *Scanner) CheckService(ctx context.Context, serviceID int64) error {
	_, err := s.checkService(ctx, serviceID)
	return err
}

// checkService is CheckService's internal form: it additionally reports whether a
// changelog resolve returned changelog.ErrRateLimited, so a sweep can surface an
// aggregate "add a token" hint.
func (s *Scanner) checkService(ctx context.Context, serviceID int64) (rateLimited bool, err error) {
	svc, err := s.services.Get(serviceID)
	if err != nil {
		return false, err
	}
	logger.Debugf("scan: checking service %d (%s) ref=%s", svc.ID, svc.Name, svc.ImageRef)
	upd, err := s.detector.Detect(ctx, svc)
	if err != nil {
		return false, err
	}
	if upd == nil {
		logger.Debugf("scan: service %d (%s) up to date", svc.ID, svc.Name)
		// Drift cleared (or never existed): forget any prior notify so a future,
		// different drift for this service fires the hint again.
		s.mu.Lock()
		delete(s.notifiedTo, serviceID)
		s.mu.Unlock()
		// An up-to-date service with no surfaced history still deserves a
		// changelog: write a synthetic current-version row (from == to) so the
		// dashboard can show what the running version shipped. Skipped when a
		// surfaced row (open/applied/dismissed) already provides one; superseded
		// rows do not count, so a self-updated service still gets a baseline.
		rl, cerr := s.ensureCurrentChangelog(ctx, svc)
		if cerr != nil {
			logger.Errorf("scan: ensure current changelog (service %d (%s)): %v", svc.ID, svc.Name, cerr)
		}
		return rl, nil // up-to-date / unmonitorable
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
		return true, nil
	case err != nil:
		logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(upd.ID, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, serr)
		}
	}
	return false, nil
}

// ensureCurrentChangelog writes a synthetic status='current' update row for an
// up-to-date service with no surfaced history, and resolves its changelog. The
// row is from == to == the current running version, so the resolver returns
// that version's own release notes. It is inert everywhere pending/applied
// logic lives (those key on 'available'/'applied'); only the dashboard's
// last-applied fallback surfaces it. A missing/failed changelog is non-fatal.
//
// Stale baselines at other digests are dropped first (the image can move out of
// band, e.g. dockbrr self-updating its own container), and a baseline is
// (re)resolved unless one is already cached for the running digest.
func (s *Scanner) ensureCurrentChangelog(ctx context.Context, svc store.Service) (rateLimited bool, err error) {
	if svc.CurrentDigest == "" {
		return false, nil // nothing to key the row on
	}
	// A prior baseline pinned to a now-superseded digest is stale: the running
	// image moved out of band (e.g. dockbrr self-updated its own container), so
	// drop it before deciding whether to write a fresh one. 'current' rows are
	// immune to supersede, so nothing else ever clears them.
	if _, derr := s.updates.DeleteStaleCurrent(svc.ID, svc.CurrentDigest); derr != nil {
		return false, derr
	}
	// A surfaced row (an open available/dismissed/rolled_back update, or an
	// applied one) already gives the dashboard a changelog; the synthetic
	// baseline is only for services with none. Superseded rows are deliberately
	// NOT surfaced, so a service whose only history is superseded, e.g. every
	// dockbrr self-update leaves one, still gets a baseline (the greyed-changelog
	// fix: gating on "any non-current row" wrongly blocked that).
	if surfaced, serr := s.updates.HasSurfacedByService(svc.ID); serr != nil {
		return false, serr
	} else if surfaced {
		return false, nil
	}
	// A baseline already RESOLVED for this digest: its changelog is cached, so
	// don't re-hit the changelog source's API. A baseline whose resolve came back
	// empty is not resolved, so it falls through and retries here (self-heals an
	// instance left with an empty baseline by an earlier miss).
	if resolved, herr := s.updates.HasResolvedCurrentAtDigest(svc.ID, svc.CurrentDigest); herr != nil {
		return false, herr
	} else if resolved {
		return false, nil
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
		return false, err
	}
	row.ID = id

	remote := registry.RemoteImage{Ref: svc.ImageRef, Digest: svc.CurrentDigest, Labels: labels}
	text, url, err := s.changelog.Resolve(ctx, row, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(id, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (current row %d): %v", id, serr)
		}
		return true, nil
	case err != nil:
		return false, err
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(id, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (current row %d): %v", id, serr)
		}
	}
	return false, nil
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

// CheckServicesFresh checks each id, invoking onDone(done, total) after every
// service completes (whether it detected drift, found nothing, or errored), and
// reports whether any changelog resolve during the sweep hit the GitHub rate
// limit (so the caller can surface an aggregate "add a token" hint).
// Per-service errors are logged and the sweep continues. onDone may be nil.
//
// reopen controls whether each service also gets the rolled_back suppression
// lifted (the "manual look-again" gesture): when true, each id goes through
// CheckServiceFresh (ReopenRolledBack + CheckService); when false, it's
// CheckService only. A sweep across every service must NEVER reopen: that
// would make a just-rolled-back update auto-apply-eligible again service-wide.
// Scoped (single-service or single-project) manual checks must reopen, so
// they match the original per-service "Check now" contract.
func (s *Scanner) CheckServicesFresh(ctx context.Context, ids []int64, reopen bool, onDone func(done, total int)) (bool, error) {
	total := len(ids)
	rateLimited := false
	for i, id := range ids {
		if ctx.Err() != nil {
			break // aborted or timed out: stop the sweep, keep partial results
		}
		var (
			rl   bool
			cerr error
		)
		if reopen {
			rl, cerr = s.checkServiceFresh(ctx, id)
		} else {
			rl, cerr = s.checkService(ctx, id)
		}
		if cerr != nil {
			logger.Errorf("scan: check service %d: %v", id, cerr)
		}
		if rl {
			rateLimited = true
		}
		if onDone != nil {
			onDone(i+1, total)
		}
	}
	return rateLimited, nil
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
