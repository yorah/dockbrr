package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"dockbrr/internal/docker"
)

// swapStartDelay gives the parent dockbrr process a moment to mark its
// self_update job terminal and close its log streams before this helper stops
// it. Without it the helper could kill the parent mid-Finish. A package var
// so tests can zero it.
var swapStartDelay = 2 * time.Second

// runSelfUpdateSwap is the detached helper entry point (`dockbrr self-update-swap`).
// It connects to the Docker socket and swaps the target container to a new image.
func runSelfUpdateSwap(args []string) error {
	fs := flag.NewFlagSet("self-update-swap", flag.ContinueOnError)
	socket := fs.String("socket", "", "Docker socket path")
	target := fs.String("target", "", "target container id to replace")
	image := fs.String("image", "", "new image reference")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *socket == "" || *target == "" || *image == "" {
		return errors.New("self-update-swap: --socket, --target and --image are all required")
	}
	dc, err := docker.New(*socket)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	logf := func(s string) { fmt.Fprintln(os.Stdout, "[self-update] "+s) }
	time.Sleep(swapStartDelay)
	logf("swapping " + *target + " -> " + *image)
	if err := dc.SwapContainer(context.Background(), *target, *image, logf); err != nil {
		logf("ERROR: " + err.Error())
		return err
	}
	logf("done")
	return nil
}
