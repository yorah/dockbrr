package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultHubBase = "https://hub.docker.com"

// RegistrySource resolves a changelog/description from registry-native metadata.
// v1 covers Docker Hub's full_description; other registries defer.
type RegistrySource struct {
	client  *http.Client
	hubBase string
}

// NewRegistrySource builds the source. A nil client gets a 10s default; empty
// hubBase defaults to https://hub.docker.com.
func NewRegistrySource(client *http.Client, hubBase string) *RegistrySource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if hubBase == "" {
		hubBase = defaultHubBase
	}
	return &RegistrySource{client: client, hubBase: hubBase}
}

func (s *RegistrySource) Name() string { return "registry-native" }

// Resolve fetches the Docker Hub full_description for a Hub-hosted repo. Non-Hub
// repos defer (empty Result) with no HTTP call.
func (s *RegistrySource) Resolve(ctx context.Context, in Input) (Result, error) {
	path, ok := hubPath(in.Repo)
	if !ok {
		return Result{}, nil // not a Docker Hub repo: defer
	}
	apiURL := fmt.Sprintf("%s/v2/repositories/%s/", s.hubBase, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			FullDescription string `json:"full_description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return Result{}, err
		}
		return Result{Text: body.FullDescription, URL: hubWebURL(path)}, nil
	case http.StatusNotFound:
		return Result{}, nil
	default:
		return Result{}, fmt.Errorf("docker hub: status %d", resp.StatusCode)
	}
}

// hubPath maps a repo reference to a Docker Hub <namespace>/<repo> path,
// reporting whether the repo is hosted on Docker Hub at all. A leading segment
// containing "." or ":" denotes a different registry host (defer).
func hubPath(repo string) (string, bool) {
	switch {
	case repo == "":
		return "", false
	case strings.HasPrefix(repo, "docker.io/"):
		repo = strings.TrimPrefix(repo, "docker.io/")
	case strings.HasPrefix(repo, "index.docker.io/"):
		repo = strings.TrimPrefix(repo, "index.docker.io/")
	default:
		first := repo
		if i := strings.Index(repo, "/"); i >= 0 {
			first = repo[:i]
		}
		if strings.ContainsAny(first, ".:") {
			return "", false
		}
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, true
}

// hubWebURL builds the Docker Hub web page for a namespace/repo path. Official
// (library/*) images use the /_/ short form.
func hubWebURL(path string) string {
	if strings.HasPrefix(path, "library/") {
		return "https://hub.docker.com/_/" + strings.TrimPrefix(path, "library/")
	}
	return "https://hub.docker.com/r/" + path
}
