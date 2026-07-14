// Package docker provides a thin wrapper around the Docker client SDK,
// exposing a normalized Container type and a Collect method for querying
// running containers.
package docker

import (
	"context"
	"fmt"

	dockerclient "github.com/docker/docker/client"
)

// Client wraps the Docker SDK client.
type Client struct {
	c *dockerclient.Client
}

// New returns a Client connected to the given unix socket path.
// The underlying SDK client is created with API version negotiation enabled,
// so no dial occurs at construction; the socket is not contacted until the
// first API call (e.g. Ping).
func New(socket string) (*Client, error) {
	host := fmt.Sprintf("unix://%s", socket)
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost(host),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// NewFromEnv returns a Client configured from standard Docker environment
// variables (DOCKER_HOST, DOCKER_TLS_VERIFY, DOCKER_CERT_PATH, DOCKER_API_VERSION).
// API version negotiation is enabled; no dial occurs at construction.
func NewFromEnv() (*Client, error) {
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// Ping contacts the Docker daemon and returns an error if it is unreachable.
func (cl *Client) Ping(ctx context.Context) error {
	_, err := cl.c.Ping(ctx)
	return err
}

// ServerVersion reports the daemon's version and the negotiated API version.
// Read-only: it queries the daemon, never mutates it.
func (cl *Client) ServerVersion(ctx context.Context) (version, apiVersion string, err error) {
	v, err := cl.c.ServerVersion(ctx)
	if err != nil {
		return "", "", err
	}
	return v.Version, v.APIVersion, nil
}

// Close releases resources held by the underlying SDK client.
func (cl *Client) Close() error {
	return cl.c.Close()
}
