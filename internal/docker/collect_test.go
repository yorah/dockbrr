package docker

import (
	"encoding/json"
	"testing"

	dcontainer "github.com/docker/docker/api/types/container"
	dimage "github.com/docker/docker/api/types/image"
)

func TestContainerFromInspect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		ctJSON  string
		imgJSON string
		want    Container
	}{
		{
			name: "compose service",
			ctJSON: `{
				"Id": "abc123",
				"Name": "/web_app_1",
				"State": {
					"Status": "running",
					"Health": {"Status": "healthy", "FailingStreak": 0, "Log": null}
				},
				"Image": "sha256:imgid111",
				"Config": {
					"Image": "nginx:1.25",
					"Labels": {
						"com.docker.compose.project": "web",
						"com.docker.compose.service": "app",
						"com.docker.compose.project.working_dir": "/srv/web",
						"com.docker.compose.project.config_files": "docker-compose.yml,override.yml"
					}
				},
				"Mounts": null,
				"NetworkSettings": null
			}`,
			imgJSON: `{
				"Id": "sha256:imgid111",
				"RepoTags": ["nginx:1.25"],
				"RepoDigests": ["nginx@sha256:aaa"]
			}`,
			want: Container{
				ID:          "abc123",
				Project:     "web",
				Service:     "app",
				WorkingDir:  "/srv/web",
				ConfigFiles: []string{"docker-compose.yml", "override.yml"},
				Name:        "web_app_1",
				ImageRef:    "nginx:1.25",
				RepoDigest:  "sha256:aaa",
				ImageID:     "sha256:imgid111",
				Pinned:      false,
				State:       "running",
				Healthcheck: true,
				Health:      "healthy",
			},
		},
		{
			name: "reads image version label from image config",
			ctJSON: `{
				"Id": "ver789",
				"Name": "/bazarr",
				"State": {"Status": "running"},
				"Image": "sha256:imgidver",
				"Config": {
					"Image": "ghcr.io/linuxserver/bazarr:latest"
				}
			}`,
			imgJSON: `{
				"Id": "sha256:imgidver",
				"RepoTags": ["ghcr.io/linuxserver/bazarr:latest"],
				"RepoDigests": ["ghcr.io/linuxserver/bazarr@sha256:5d916d074042"],
				"Config": {
					"Labels": {"org.opencontainers.image.version": "1.6.0-ls354"}
				}
			}`,
			want: Container{
				ID:         "ver789",
				Name:       "bazarr",
				ImageRef:   "ghcr.io/linuxserver/bazarr:latest",
				RepoDigest: "sha256:5d916d074042",
				ImageID:    "sha256:imgidver",
				Version:    "1.6.0-ls354",
				State:      "running",
			},
		},
		{
			name: "standalone",
			ctJSON: `{
				"Id": "def456",
				"Name": "/redis",
				"State": {
					"Status": "running"
				},
				"Image": "sha256:imgid222",
				"Config": {
					"Image": "redis:7",
					"Labels": {}
				},
				"Mounts": null,
				"NetworkSettings": null
			}`,
			imgJSON: `{
				"Id": "sha256:imgid222",
				"RepoTags": ["redis:7"],
				"RepoDigests": ["redis@sha256:bbb"]
			}`,
			want: Container{
				ID:          "def456",
				Project:     "",
				Service:     "",
				Name:        "redis",
				ImageRef:    "redis:7",
				RepoDigest:  "sha256:bbb",
				ImageID:     "sha256:imgid222",
				Pinned:      false,
				State:       "running",
				Healthcheck: false,
				Health:      "",
			},
		},
		{
			name: "pinned",
			ctJSON: `{
				"Id": "ghi789",
				"Name": "/myapp",
				"State": {
					"Status": "running"
				},
				"Image": "sha256:imgid333",
				"Config": {
					"Image": "app@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
					"Labels": {}
				},
				"Mounts": null,
				"NetworkSettings": null
			}`,
			imgJSON: `{
				"Id": "sha256:imgid333",
				"RepoTags": [],
				"RepoDigests": ["app@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"]
			}`,
			want: Container{
				ID:          "ghi789",
				Name:        "myapp",
				ImageRef:    "app@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
				RepoDigest:  "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
				ImageID:     "sha256:imgid333",
				Pinned:      true,
				State:       "running",
				Healthcheck: false,
				Health:      "",
			},
		},
		{
			name: "no RepoDigests",
			ctJSON: `{
				"Id": "jkl012",
				"Name": "/local",
				"State": {
					"Status": "exited"
				},
				"Image": "sha256:imgid444",
				"Config": {
					"Image": "local/myimage:latest",
					"Labels": {}
				},
				"Mounts": null,
				"NetworkSettings": null
			}`,
			imgJSON: `{
				"Id": "sha256:imgid444",
				"RepoTags": ["local/myimage:latest"],
				"RepoDigests": []
			}`,
			want: Container{
				ID:          "jkl012",
				Name:        "local",
				ImageRef:    "local/myimage:latest",
				RepoDigest:  "",
				ImageID:     "sha256:imgid444",
				Pinned:      false,
				State:       "exited",
				Healthcheck: false,
				Health:      "",
			},
		},
		{
			name: "no healthcheck",
			ctJSON: `{
				"Id": "mno345",
				"Name": "/worker",
				"State": {
					"Status": "running"
				},
				"Image": "sha256:imgid555",
				"Config": {
					"Image": "worker:2.0",
					"Labels": {}
				},
				"Mounts": null,
				"NetworkSettings": null
			}`,
			imgJSON: `{
				"Id": "sha256:imgid555",
				"RepoTags": ["worker:2.0"],
				"RepoDigests": ["worker@sha256:ccc"]
			}`,
			want: Container{
				ID:          "mno345",
				Name:        "worker",
				ImageRef:    "worker:2.0",
				RepoDigest:  "sha256:ccc",
				ImageID:     "sha256:imgid555",
				Pinned:      false,
				State:       "running",
				Healthcheck: false,
				Health:      "",
			},
		},
		{
			// Apply/rollback writes a temp dockbrr-rollback-*.yml override and
			// passes it as an extra -f; compose bakes it into the config_files
			// label, then dockbrr deletes it. It must be stripped here so its
			// later absence can't flag the project "unmanaged" (apply-refused).
			name: "dockbrr rollback override stripped from config files",
			ctJSON: `{
				"Id": "pin789",
				"Name": "/smoke-web",
				"State": {"Status": "running"},
				"Image": "sha256:imgid777",
				"Config": {
					"Image": "nginx:1.31.2@sha256:ec4",
					"Labels": {
						"com.docker.compose.project": "dockbrr-smoke",
						"com.docker.compose.service": "web",
						"com.docker.compose.project.working_dir": "/home/u/smoke",
						"com.docker.compose.project.config_files": "/home/u/smoke/compose.yml,/home/u/smoke/dockbrr-rollback-3931043621.yml"
					}
				}
			}`,
			imgJSON: `{
				"Id": "sha256:imgid777",
				"RepoTags": ["nginx:1.31.2"],
				"RepoDigests": ["nginx@sha256:ec4"]
			}`,
			want: Container{
				ID:          "pin789",
				Project:     "dockbrr-smoke",
				Service:     "web",
				WorkingDir:  "/home/u/smoke",
				ConfigFiles: []string{"/home/u/smoke/compose.yml"},
				Name:        "smoke-web",
				ImageRef:    "nginx:1.31.2@sha256:ec4",
				RepoDigest:  "sha256:ec4",
				ImageID:     "sha256:imgid777",
				Pinned:      true,
				State:       "running",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ct dcontainer.InspectResponse
			if err := json.Unmarshal([]byte(tc.ctJSON), &ct); err != nil {
				t.Fatalf("unmarshal container inspect: %v", err)
			}

			var img dimage.InspectResponse
			if err := json.Unmarshal([]byte(tc.imgJSON), &img); err != nil {
				t.Fatalf("unmarshal image inspect: %v", err)
			}

			got := containerFromInspect(ct, img)

			if got.ID != tc.want.ID {
				t.Errorf("ID: got %q, want %q", got.ID, tc.want.ID)
			}
			if got.Project != tc.want.Project {
				t.Errorf("Project: got %q, want %q", got.Project, tc.want.Project)
			}
			if got.Service != tc.want.Service {
				t.Errorf("Service: got %q, want %q", got.Service, tc.want.Service)
			}
			if got.WorkingDir != tc.want.WorkingDir {
				t.Errorf("WorkingDir: got %q, want %q", got.WorkingDir, tc.want.WorkingDir)
			}
			if (got.ConfigFiles == nil) != (tc.want.ConfigFiles == nil) {
				t.Errorf("ConfigFiles nil-ness: got %v, want %v", got.ConfigFiles, tc.want.ConfigFiles)
			} else if len(got.ConfigFiles) != len(tc.want.ConfigFiles) {
				t.Errorf("ConfigFiles len: got %d, want %d", len(got.ConfigFiles), len(tc.want.ConfigFiles))
			} else {
				for i := range tc.want.ConfigFiles {
					if got.ConfigFiles[i] != tc.want.ConfigFiles[i] {
						t.Errorf("ConfigFiles[%d]: got %q, want %q", i, got.ConfigFiles[i], tc.want.ConfigFiles[i])
					}
				}
			}
			if got.Name != tc.want.Name {
				t.Errorf("Name: got %q, want %q", got.Name, tc.want.Name)
			}
			if got.ImageRef != tc.want.ImageRef {
				t.Errorf("ImageRef: got %q, want %q", got.ImageRef, tc.want.ImageRef)
			}
			if got.RepoDigest != tc.want.RepoDigest {
				t.Errorf("RepoDigest: got %q, want %q", got.RepoDigest, tc.want.RepoDigest)
			}
			if got.ImageID != tc.want.ImageID {
				t.Errorf("ImageID: got %q, want %q", got.ImageID, tc.want.ImageID)
			}
			if got.Pinned != tc.want.Pinned {
				t.Errorf("Pinned: got %v, want %v", got.Pinned, tc.want.Pinned)
			}
			if got.State != tc.want.State {
				t.Errorf("State: got %q, want %q", got.State, tc.want.State)
			}
			if got.Healthcheck != tc.want.Healthcheck {
				t.Errorf("Healthcheck: got %v, want %v", got.Healthcheck, tc.want.Healthcheck)
			}
			if got.Health != tc.want.Health {
				t.Errorf("Health: got %q, want %q", got.Health, tc.want.Health)
			}
		})
	}
}
