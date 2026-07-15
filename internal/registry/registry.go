// Package registry resolves remote image references to digests + OCI metadata
// using go-containerregistry. It is anonymous-first: stored credentials are
// consulted only when the registry returns 401. It performs network reads only.
package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"dockbrr/internal/logger"
)

// Platform identifies the OS/Arch used to resolve a multi-arch image.
type Platform struct {
	OS   string
	Arch string
}

// HostPlatform returns the platform of the process host. On a single Docker
// host this matches the architecture of the running containers.
func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// RemoteImage is the resolved view of a remote reference.
type RemoteImage struct {
	Ref            string
	Digest         string
	PlatformDigest string
	MediaType      string
	Labels         map[string]string
	BuiltAt        time.Time
	OS             string
	Architecture   string
}

// CredentialStore yields stored credentials for a registry host. The Phase-6
// table-backed implementation satisfies it; a nil store means anonymous-only.
type CredentialStore interface {
	Lookup(registryHost string) (username, password string, ok bool)
}

// Resolver resolves references via go-containerregistry.
type Resolver struct {
	creds CredentialStore
}

// NewResolver builds a Resolver. A nil creds store resolves anonymously only.
func NewResolver(creds CredentialStore) *Resolver {
	return &Resolver{creds: creds}
}

// Resolve fetches the descriptor for ref, platform-resolving the manifest to
// read the per-arch config labels. The returned Digest is the registry-served
// digest for the tag (the compare/pull target); PlatformDigest is the
// platform image manifest digest (equal for single-arch).
func (r *Resolver) Resolve(ctx context.Context, ref string, plat Platform) (RemoteImage, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return RemoteImage{}, fmt.Errorf("registry: parse ref %q: %w", ref, err)
	}
	platform := v1.Platform{OS: plat.OS, Architecture: plat.Arch}

	logger.Tracef("registry: resolve %s (%s/%s)", ref, plat.OS, plat.Arch)
	desc, err := r.get(ctx, parsed, platform)
	if err != nil {
		return RemoteImage{}, err
	}
	logger.Tracef("registry: resolved %s -> %s", ref, desc.Digest.String())

	out := RemoteImage{
		Ref:       ref,
		Digest:    desc.Digest.String(),
		MediaType: string(desc.MediaType),
	}

	img, err := desc.Image()
	if err != nil {
		return RemoteImage{}, fmt.Errorf("registry: resolve image %q: %w", ref, err)
	}
	if h, err := img.Digest(); err == nil {
		out.PlatformDigest = h.String()
	}
	// Labels are best-effort by design: a config-fetch failure is diagnosable
	// but does not hard-fail Resolve (digest compare still works).
	if cf, err := img.ConfigFile(); err == nil && cf != nil {
		out.Labels = maps.Clone(cf.Config.Labels) // defensive copy, not an alias
		out.BuiltAt = cf.Created.Time
		out.OS = cf.OS
		out.Architecture = cf.Architecture
	} else if err != nil {
		logger.Warnf("registry: config %q: %v (labels skipped)", ref, err)
	}
	if out.PlatformDigest == "" {
		out.PlatformDigest = out.Digest
	}
	return out, nil
}

// get fetches the descriptor anonymously, retrying with stored credentials on
// a 401 when a credential store is configured.
func (r *Resolver) get(ctx context.Context, ref name.Reference, platform v1.Platform) (*remote.Descriptor, error) {
	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithPlatform(platform),
	)
	if err == nil {
		return desc, nil
	}
	if !IsUnauthorized(err) || r.creds == nil {
		return nil, fmt.Errorf("registry: get %q: %w", ref.String(), err)
	}
	user, pass, ok := r.creds.Lookup(ref.Context().RegistryStr())
	if !ok {
		return nil, fmt.Errorf("registry: get %q: %w", ref.String(), err)
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})
	desc, err = remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuth(auth),
		remote.WithPlatform(platform),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: get %q (with creds): %w", ref.String(), err)
	}
	return desc, nil
}

// ListTags lists the repository's tags, anonymous-first with the same
// credential-retry-on-401 behavior as Resolve. Used by the detector's semver
// scan; a failure is non-fatal to detection (digest compare still runs).
func (r *Resolver) ListTags(ctx context.Context, repo string) ([]string, error) {
	rep, err := name.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("registry: parse repo %q: %w", repo, err)
	}
	tags, err := remote.List(rep, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	if err == nil {
		return tags, nil
	}
	if !IsUnauthorized(err) || r.creds == nil {
		return nil, fmt.Errorf("registry: list %q: %w", repo, err)
	}
	user, pass, ok := r.creds.Lookup(rep.RegistryStr())
	if !ok {
		return nil, fmt.Errorf("registry: list %q: %w", repo, err)
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})
	tags, err = remote.List(rep, remote.WithContext(ctx), remote.WithAuth(auth))
	if err != nil {
		return nil, fmt.Errorf("registry: list %q (with creds): %w", repo, err)
	}
	return tags, nil
}

// Head resolves ref to the registry-served manifest digest without fetching the
// manifest body or config blob. It is the cheap path used by the detector's
// floating-tag reverse version-naming scan (many tags, digest match only).
// Anonymous-first with the same credential-retry-on-401 behavior as Resolve.
func (r *Resolver) Head(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("registry: parse ref %q: %w", ref, err)
	}
	desc, err := remote.Head(parsed, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	if err == nil {
		return desc.Digest.String(), nil
	}
	if !IsUnauthorized(err) || r.creds == nil {
		return "", fmt.Errorf("registry: head %q: %w", ref, err)
	}
	user, pass, ok := r.creds.Lookup(parsed.Context().RegistryStr())
	if !ok {
		return "", fmt.Errorf("registry: head %q: %w", ref, err)
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})
	desc, err = remote.Head(parsed, remote.WithContext(ctx), remote.WithAuth(auth))
	if err != nil {
		return "", fmt.Errorf("registry: head %q (with creds): %w", ref, err)
	}
	return desc.Digest.String(), nil
}

// IsRateLimited reports whether err is a registry 429 (Too Many Requests).
func IsRateLimited(err error) bool {
	var terr *transport.Error
	return errors.As(err, &terr) && terr.StatusCode == http.StatusTooManyRequests
}

// IsUnauthorized reports whether err is a registry 401 (Unauthorized).
func IsUnauthorized(err error) bool {
	var terr *transport.Error
	return errors.As(err, &terr) && terr.StatusCode == http.StatusUnauthorized
}
