package docker

import (
	"bytes"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

func TestTailArg(t *testing.T) {
	if got := tailArg(0); got != "all" {
		t.Fatalf("tailArg(0) = %q, want all", got)
	}
	if got := tailArg(500); got != "500" {
		t.Fatalf("tailArg(500) = %q, want 500", got)
	}
	if got := tailArg(-5); got != "all" {
		t.Fatalf("tailArg(-5) = %q, want all (non-positive clamps to all)", got)
	}
}

func TestDecodeLogStream(t *testing.T) {
	// Build a multiplexed docker log stream (stdout + stderr framed).
	var raw bytes.Buffer
	w := stdcopy.NewStdWriter(&raw, stdcopy.Stdout)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	e := stdcopy.NewStdWriter(&raw, stdcopy.Stderr)
	if _, err := e.Write([]byte("oops\n")); err != nil {
		t.Fatal(err)
	}
	got, err := decodeLogStream(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\noops\n" {
		t.Fatalf("decodeLogStream = %q, want interleaved stdout+stderr", got)
	}
}
