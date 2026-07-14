package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// Service is a parsed compose service (name + configured image ref).
// NetworkMode/Ipc/Pid carry the raw compose value (e.g. "service:gluetun")
// when the service borrows another service's namespace, see
// NamespaceDependents.
type Service struct {
	Name        string
	Image       string
	NetworkMode string
	Ipc         string
	Pid         string
}

// Project is the read-only parse result: the project name and its services.
type Project struct {
	Name     string
	Services []Service
}

// Parse loads the compose files with compose-go and returns the services sorted
// by name. It reads the files itself and performs no network or Docker access
// (pure read). Validation and consistency checks are skipped so an incomplete
// dev stack still yields a preview.
func Parse(ctx context.Context, workingDir string, configFiles []string) (Project, error) {
	if len(configFiles) == 0 {
		return Project{}, fmt.Errorf("compose: no config files")
	}
	cfgs := make([]types.ConfigFile, 0, len(configFiles))
	for _, f := range configFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return Project{}, fmt.Errorf("compose: read %s: %w", f, err)
		}
		cfgs = append(cfgs, types.ConfigFile{Filename: f, Content: b})
	}
	details := types.ConfigDetails{
		WorkingDir:  workingDir,
		ConfigFiles: cfgs,
		Environment: buildEnv(workingDir),
	}
	proj, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName(projectName(workingDir), false)
		o.SkipValidation = true
		o.SkipConsistencyCheck = true
	})
	if err != nil {
		return Project{}, fmt.Errorf("compose: load: %w", err)
	}
	out := Project{Name: proj.Name}
	for name, svc := range proj.Services {
		out.Services = append(out.Services, Service{
			Name: name, Image: svc.Image,
			NetworkMode: svc.NetworkMode, Ipc: svc.Ipc, Pid: svc.Pid,
		})
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	return out, nil
}

// buildEnv assembles the interpolation environment the way docker compose does:
// the process environment (highest precedence) overlaid on the project's .env
// file (defaults). Without this, ${VAR} image refs resolve to a blank string,
// which breaks drift detection and the apply precheck for var-based stacks,
// while the exec'd `docker compose` (apply path) DOES load .env, so parse and
// apply would otherwise disagree. A missing/unreadable .env is not an error.
func buildEnv(workingDir string) types.Mapping {
	env := types.Mapping{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	if workingDir == "" {
		return env
	}
	envPath := filepath.Join(workingDir, ".env")
	if _, err := os.Stat(envPath); err != nil {
		return env
	}
	// GetEnvFromFile interpolates the file against the process env; overlay the
	// results as DEFAULTS only, so a shell-set value wins over the .env (compose
	// precedence).
	fromFile, err := dotenv.GetEnvFromFile(env, []string{envPath})
	if err != nil {
		return env
	}
	for k, v := range fromFile {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	return env
}

// HashFiles returns the sha256 hex of the concatenated bytes of the config
// files, used as the snapshot's compose_file_hash.
func HashFiles(configFiles []string) (string, error) {
	h := sha256.New()
	for _, f := range configFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("compose: hash read %s: %w", f, err)
		}
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// projectName derives a compose-legal project name from the working dir base.
// compose-go requires a lowercase name; empty falls back to "dockbrr".
func projectName(workingDir string) string {
	base := strings.ToLower(filepath.Base(workingDir))
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	name = strings.TrimLeft(name, "_-")
	if name == "" {
		return "dockbrr"
	}
	return name
}
