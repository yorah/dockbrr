// Package discovery groups Docker containers into projects+services and
// reconciles the result into the store.
package discovery

import (
	"context"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"dockbrr/internal/compose"
	"dockbrr/internal/detect"
	"dockbrr/internal/discovery/dockername"
	"dockbrr/internal/docker"
	"dockbrr/internal/store"
)

// DiscoveredService is a normalized view of one compose service (or standalone
// container-as-service) produced by Group.
type DiscoveredService struct {
	Name           string
	ContainerIDs   []string
	ImageRef       string
	CurrentDigest  string // maps from docker.Container.RepoDigest
	CurrentImageID string // maps from docker.Container.ImageID
	ImageVersion   string // maps from docker.Container.Version
	Pinned         bool
	State          string
	Healthcheck    bool
}

// DiscoveredProject groups services that share a compose project label, or a
// single standalone container expressed as a one-service project.
type DiscoveredProject struct {
	Name        string
	Kind        string // "compose" | "standalone"
	WorkingDir  string
	ConfigFiles []string
	Services    []DiscoveredService
}

// Collector abstracts container enumeration; *docker.Client satisfies it.
type Collector interface {
	Collect(ctx context.Context) ([]docker.Container, error)
}

// Group normalizes cs into DiscoveredProjects following these rules:
//   - Non-empty Project label → compose project; replicas (same Project+Service)
//     merge their ContainerIDs; WorkingDir/ConfigFiles taken from the first
//     container that carries them.
//   - Empty Project label → standalone project named after container.Name.
//
// Output is deterministically sorted: projects by Name, services by Name.
func Group(cs []docker.Container) []DiscoveredProject {
	type projectMeta struct {
		kind        string
		workingDir  string
		configFiles []string
		services    map[string]*DiscoveredService
	}

	index := make(map[string]*projectMeta)

	for _, c := range cs {
		if c.Project != "" {
			// Compose project.
			meta, ok := index[c.Project]
			if !ok {
				meta = &projectMeta{
					kind:        "compose",
					workingDir:  c.WorkingDir,
					configFiles: c.ConfigFiles,
					services:    make(map[string]*DiscoveredService),
				}
				index[c.Project] = meta
			}
			// Inherit WorkingDir / ConfigFiles from the first container carrying them.
			if meta.workingDir == "" && c.WorkingDir != "" {
				meta.workingDir = c.WorkingDir
			}
			if len(meta.configFiles) == 0 && len(c.ConfigFiles) > 0 {
				meta.configFiles = c.ConfigFiles
			}
			// Skip containers with no service label: they must not produce a
			// DiscoveredService{Name:""}.
			if c.Service == "" {
				continue
			}
			// Merge replica or create new service entry.
			svc, exists := meta.services[c.Service]
			if !exists {
				svc = &DiscoveredService{
					Name:           c.Service,
					ImageRef:       c.ImageRef,
					CurrentDigest:  c.RepoDigest,
					CurrentImageID: c.ImageID,
					ImageVersion:   c.Version,
					Pinned:         c.Pinned,
					State:          c.State,
					Healthcheck:    c.Healthcheck,
				}
				meta.services[c.Service] = svc
			}
			svc.ContainerIDs = append(svc.ContainerIDs, c.ID)
		} else {
			// Standalone: project name = service name = container name.
			name := c.Name
			if _, ok := index[name]; !ok {
				index[name] = &projectMeta{
					kind: "standalone",
					services: map[string]*DiscoveredService{
						name: {
							Name:           name,
							ContainerIDs:   []string{c.ID},
							ImageRef:       c.ImageRef,
							CurrentDigest:  c.RepoDigest,
							CurrentImageID: c.ImageID,
							ImageVersion:   c.Version,
							Pinned:         c.Pinned,
							State:          c.State,
							Healthcheck:    c.Healthcheck,
						},
					},
				}
			}
		}
	}

	// Sort project names for deterministic output.
	names := make([]string, 0, len(index))
	for n := range index {
		names = append(names, n)
	}
	sort.Strings(names)

	result := make([]DiscoveredProject, 0, len(names))
	for _, n := range names {
		meta := index[n]

		// Sort service names.
		svcNames := make([]string, 0, len(meta.services))
		for s := range meta.services {
			svcNames = append(svcNames, s)
		}
		sort.Strings(svcNames)

		svcs := make([]DiscoveredService, 0, len(svcNames))
		for _, s := range svcNames {
			svcs = append(svcs, *meta.services[s])
		}

		result = append(result, DiscoveredProject{
			Name:        n,
			Kind:        meta.kind,
			WorkingDir:  meta.workingDir,
			ConfigFiles: meta.configFiles,
			Services:    svcs,
		})
	}

	return result
}

// Reconciler syncs discovered containers into the store on demand.
type Reconciler struct {
	src      Collector
	projects *store.Projects
	services *store.Services
	hostID   int64
	// settings gates the auto-prune pass (auto_remove_gone / gone_grace_seconds).
	// nil disables pruning entirely (e.g. in tests that don't exercise it).
	settings *store.Settings
	// states caches remote-resolution outcomes. When a service's running digest
	// changes across reconciles (recreate/redeploy), its (repo,tag) cache entry
	// is invalidated so the next detect scan does a full network resolve
	// instead of the short-circuit. nil disables invalidation entirely (e.g. in
	// tests that don't exercise it).
	states *store.RemoteStates
	// lastSig fingerprints the last reconciled discovery surface so Reconcile
	// can report whether anything observable changed (dedups the "reconciled"
	// refresh hint). Reconcile has a single caller (reconcileLoop) on one
	// goroutine, so no lock is needed. Zero value "" forces a changed=true on
	// the first cycle even for an empty host (its sig is never "").
	lastSig string
}

// NewReconciler constructs a Reconciler. settings may be nil to disable the
// auto-prune pass (gone-service / empty-project hard deletion). states may be
// nil to disable detect-cache invalidation on running-digest change.
func NewReconciler(src Collector, projects *store.Projects, services *store.Services, hostID int64, settings *store.Settings, states *store.RemoteStates) *Reconciler {
	return &Reconciler{src: src, projects: projects, services: services, hostID: hostID, settings: settings, states: states}
}

// Reconcile runs one discovery cycle:
//  1. Collect containers from src.
//  2. Group into projects.
//  3. Upsert each project's services into the store. For a project stored as
//     "manual", the project ROW (source/working_dir/config_files) is
//     user-owned and left untouched. Only its services are refreshed with
//     live digests/container ids/state.
//  4. Mark gone any service that belongs to a stored "discovered" project but
//     is absent from the freshly-discovered set. Manual-source projects are
//     never subject to mark-gone: their compose services may legitimately not
//     be running yet.
func (r *Reconciler) Reconcile(ctx context.Context) (changed bool, err error) {
	cs, err := r.src.Collect(ctx)
	if err != nil {
		return false, err
	}

	groups := Group(cs)

	// Build the present set: project name → set of service names.
	present := make(map[string]map[string]bool, len(groups))
	for _, g := range groups {
		svcSet := make(map[string]bool, len(g.Services))
		for _, s := range g.Services {
			svcSet[s.Name] = true
		}
		present[g.Name] = svcSet
	}

	// Fetch all stored projects once, used to refresh services inside manual
	// projects (without touching their rows) and for the mark-gone pass below.
	allProjects, err := r.projects.List()
	if err != nil {
		return false, err
	}

	// Build name → id for stored manual projects so their SERVICES can be
	// refreshed without touching the manual project row itself.
	manualIDs := make(map[string]int64, len(allProjects))
	for _, p := range allProjects {
		if p.Source == "manual" {
			manualIDs[p.Name] = p.ID
		}
	}

	// Upsert discovered projects and their services.
	now := time.Now().UTC()
	for _, g := range groups {
		var pid int64
		if mid, isManual := manualIDs[g.Name]; isManual {
			// Manual project: the row (source/config_files/working_dir) is
			// user-owned, refresh only its services below.
			pid = mid
		} else {
			var err error
			pid, err = r.projects.Upsert(store.Project{
				HostID:            r.hostID,
				Kind:              g.Kind,
				Name:              g.Name,
				WorkingDir:        g.WorkingDir,
				ConfigFiles:       g.ConfigFiles,
				Source:            "discovered",
				AutoUpdateEnabled: r.settings != nil && r.settings.GetBoolDefault("default_auto_update_enabled", false),
				LastSyncedAt:      &now,
			})
			if err != nil {
				return false, err
			}
		}

		// A compose project is "unmanaged" once its recorded config files are
		// missing or unreadable, apply must be refused (design §7). Standalone
		// projects have no config files and are never flagged.
		if g.Kind == "compose" && len(g.ConfigFiles) > 0 {
			missing := false
			for _, f := range g.ConfigFiles {
				if _, err := os.Stat(f); err != nil {
					missing = true
					break
				}
			}
			if err := r.projects.SetUnmanaged(pid, missing); err != nil {
				return false, err
			}
		}

		// Loose grouping: a standalone container whose name Docker auto-assigned
		// (adjective_surname) is throwaway noise. Compose projects are never
		// loose. Recomputed each reconcile so pre-existing rows get corrected.
		if err := r.projects.SetAutoNamed(pid, g.Kind == "standalone" && dockername.IsDockerAssigned(g.Name)); err != nil {
			return false, err
		}

		// Drift: a service is drifted when the image it is actually running
		// differs from what its compose file declares (e.g. a runtime-only
		// digest pin the file doesn't carry, or an out-of-band edit). Reuses
		// the read-only compose parser.
		declared := map[string]string{} // service name -> declared image ref
		if g.Kind == "compose" && len(g.ConfigFiles) > 0 {
			if pj, perr := compose.Parse(ctx, g.WorkingDir, g.ConfigFiles); perr == nil {
				for _, s := range pj.Services {
					declared[s.Name] = s.Image
				}
			}
			// parse error: leave declared empty -> nothing marked drifted this cycle.
		}

		// Prior stored digests, keyed by service name, for detect-cache
		// invalidation below. Only fetched when invalidation is enabled (states
		// non-nil) to avoid an extra query per project on the hot path.
		var priorDigest map[string]string
		if r.states != nil {
			storedSvcs, err := r.services.ListByProject(pid)
			if err != nil {
				return false, err
			}
			priorDigest = make(map[string]string, len(storedSvcs))
			for _, sv := range storedSvcs {
				priorDigest[sv.Name] = sv.CurrentDigest
			}
		}

		for _, s := range g.Services {
			if r.states != nil {
				prev := priorDigest[s.Name] // "" if new/unknown service
				if prev != "" && prev != s.CurrentDigest {
					repo, tag := detect.SplitRef(s.ImageRef)
					_ = r.states.Invalidate(repo, tag) // best-effort: never fail the reconcile
				}
			}
			if _, err := r.services.Upsert(store.Service{
				ProjectID:      pid,
				Name:           s.Name,
				ContainerIDs:   s.ContainerIDs,
				ImageRef:       s.ImageRef,
				CurrentDigest:  s.CurrentDigest,
				CurrentImageID: s.CurrentImageID,
				ImageVersion:   s.ImageVersion,
				Pinned:         s.Pinned,
				Drifted:        declaredDiffers(declared[s.Name], s.ImageRef),
				State:          s.State,
				Healthcheck:    s.Healthcheck,
				// AutoUpdateEnabled intentionally nil, discovery must not override user setting.
			}); err != nil {
				return false, err
			}
		}
	}

	for _, p := range allProjects {
		if p.Source != "discovered" {
			continue
		}
		storedSvcs, err := r.services.ListByProject(p.ID)
		if err != nil {
			return false, err
		}
		presentSvcs := present[p.Name] // nil map → all services gone
		for _, id := range goneServiceIDs(storedSvcs, presentSvcs) {
			if err := r.services.MarkGone(id); err != nil {
				return false, err
			}
		}
	}

	// Auto-prune: hard-delete services that have been gone longer than the grace,
	// then discovered projects left empty. Off by default-safe: only runs when
	// the user opted in (default on). gone_grace is read live each cycle.
	if r.settings != nil && r.settings.GetBoolDefault("auto_remove_gone", true) {
		graceSecs := settingIntDefault(r.settings, "gone_grace_seconds", 3600)
		if graceSecs < 0 {
			// A negative grace would push the cutoff into the future
			// (now - negative = future), sweeping up every gone service
			// including ones that only just went gone. Clamp to 0.
			graceSecs = 0
		}
		grace := time.Duration(graceSecs) * time.Second
		cutoff := time.Now().UTC().Add(-grace)
		all, err := r.projects.List()
		if err != nil {
			return changed, err
		}
		for _, p := range all {
			// Defense-in-depth: only discovered projects are eligible for
			// service-delete (mark-gone only ever runs for source=="discovered"
			// projects above, so this loop is currently inert for manual
			// projects, but gate it explicitly anyway).
			if p.Source != "discovered" {
				continue
			}
			svcs, err := r.services.ListByProject(p.ID)
			if err != nil {
				return changed, err
			}
			for _, sv := range svcs {
				if sv.State == "gone" && sv.GoneSince != nil && sv.GoneSince.Before(cutoff) {
					if err := r.services.Delete(sv.ID); err != nil {
						return changed, err
					}
					changed = true
				}
			}
			// Re-check emptiness after deletions; only discovered projects are pruned.
			remaining, err := r.services.ListByProject(p.ID)
			if err != nil {
				return changed, err
			}
			if len(remaining) == 0 {
				if err := r.projects.Delete(p.ID); err != nil {
					return changed, err
				}
				changed = true
			}
		}
	}

	// Report whether the discovered surface changed since the last cycle so the
	// caller can skip the refresh hint on a no-op reconcile. A service going
	// gone drops it from groups, so its removal is captured here too. Any prune
	// deletion above already forces changed=true regardless of the signature
	// comparison (store-side deletions aren't reflected in the live discovery
	// signature), so this must OR into the existing value rather than overwrite it.
	sig := discoverySig(groups)
	changed = changed || sig != r.lastSig
	r.lastSig = sig
	return changed, nil
}

// settingIntDefault reads an integer setting, falling back to def when the key
// is absent or not a valid integer. Minimal local helper: the store has no
// typed int accessor.
func settingIntDefault(s *store.Settings, key string, def int) int {
	v, err := s.Get(key)
	if err != nil || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// discoverySig fingerprints the dashboard-relevant surface of a discovery cycle
// (project identity + each service's identity/state/digest/containers) so the
// reconciler can detect a no-op cycle. groups is already sorted deterministically
// by Group; container ids are sorted here defensively. It does NOT capture the
// on-disk config-file stat used for the unmanaged flag. A config file removed
// without any container change won't re-fire the hint until the next real change.
func discoverySig(groups []DiscoveredProject) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(g.Name)
		b.WriteByte('|')
		b.WriteString(g.Kind)
		b.WriteByte('|')
		b.WriteString(strings.Join(g.ConfigFiles, ","))
		b.WriteByte('\n')
		for _, s := range g.Services {
			ids := append([]string(nil), s.ContainerIDs...)
			sort.Strings(ids)
			b.WriteString(s.Name)
			b.WriteByte('\x1f')
			b.WriteString(s.State)
			b.WriteByte('\x1f')
			b.WriteString(s.ImageRef)
			b.WriteByte('\x1f')
			b.WriteString(s.CurrentDigest)
			b.WriteByte('\x1f')
			b.WriteString(strconv.FormatBool(s.Pinned))
			b.WriteByte('\x1f')
			b.WriteString(strings.Join(ids, ","))
			b.WriteByte('\n')
		}
	}
	h := fnv.New64a()
	h.Write([]byte(b.String()))
	// Prefix keeps a non-empty result even for an empty host, so the first
	// reconcile (lastSig == "") always reports changed.
	return "d" + strconv.FormatUint(h.Sum64(), 16)
}

// declaredDiffers reports whether a service's running ref diverges from its
// compose-declared image. Empty declared (standalone / parse failure / service
// absent from file) is never drift.
func declaredDiffers(declared, running string) bool {
	if declared == "" {
		return false
	}
	return declared != running
}

// goneServiceIDs returns the IDs of stored services whose names are absent from
// the present set. It is a pure function with no I/O.
func goneServiceIDs(stored []store.Service, present map[string]bool) []int64 {
	var ids []int64
	for _, s := range stored {
		if !present[s.Name] {
			ids = append(ids, s.ID)
		}
	}
	return ids
}
