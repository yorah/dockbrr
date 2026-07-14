package compose

import (
	"strings"
	"testing"
)

func TestBuildArgsIncludesProjectName(t *testing.T) {
	spec := RunSpec{
		ConfigFiles: []string{"/srv/app/docker-compose.yml"},
		ProjectDir:  "/srv/app",
		ProjectName: "customname",
		Verb:        "pull",
	}
	got, err := Preview(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := "docker compose -f /srv/app/docker-compose.yml --project-directory /srv/app -p customname pull"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildArgsOmitsEmptyProjectName(t *testing.T) {
	spec := RunSpec{ConfigFiles: []string{"/a/c.yml"}, ProjectDir: "/a", Verb: "pull"}
	got, err := Preview(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, " -p ") {
		t.Errorf("empty ProjectName must not emit -p: %q", got)
	}
}

func TestPullAndUpSpecCarryProjectName(t *testing.T) {
	p := PullSpec([]string{"/a/c.yml"}, "/a", "proj", "service", "web")
	if p.ProjectName != "proj" {
		t.Errorf("PullSpec dropped project name")
	}
	u := UpSpec([]string{"/a/c.yml"}, "/a", "proj", "project", "")
	if u.ProjectName != "proj" {
		t.Errorf("UpSpec dropped project name")
	}
}
