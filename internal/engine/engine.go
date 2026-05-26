package engine

import (
	"context"
	"io"
	"time"
)

// ContainerInfo holds runtime information about a container.
type ContainerInfo struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	State  string            `json:"state"`  // running, exited, paused, created
	Status string            `json:"status"` // "Up 2 hours", "Exited (0) 3 minutes ago"
	Ports  []string          `json:"ports"`
	Labels map[string]string `json:"labels"`
	Stack  string            `json:"stack"` // dill.stack label value, empty if not managed
}

// ImageInfo holds information about a local image.
type ImageInfo struct {
	ID      string    `json:"id"`
	Tags    []string  `json:"tags"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

// Engine defines the interface brine uses to interact with a container runtime.
// The local implementation talks to Docker; future remote agents implement the
// same interface over an HTTP transport.
type Engine interface {
	// ListContainers returns all containers (running and stopped).
	ListContainers(ctx context.Context) ([]ContainerInfo, error)

	// InspectContainer returns details for a single container by ID or name.
	InspectContainer(ctx context.Context, id string) (*ContainerInfo, error)

	// StartContainer starts a stopped container.
	StartContainer(ctx context.Context, id string) error

	// StopContainer stops a running container.
	StopContainer(ctx context.Context, id string) error

	// RestartContainer restarts a container.
	RestartContainer(ctx context.Context, id string) error

	// ContainerLogs returns a reader for the container's log output.
	// The caller is responsible for closing the reader.
	// If follow is true, the stream stays open until ctx is cancelled.
	// tail=0 means all logs; tail>0 returns only the last N lines.
	ContainerLogs(ctx context.Context, id string, follow bool, tail int) (io.ReadCloser, error)

	// ListImages returns all locally available images.
	ListImages(ctx context.Context) ([]ImageInfo, error)

	// PullImage pulls an image from its registry, streaming progress to the returned reader.
	// The caller must close the reader to release resources.
	PullImage(ctx context.Context, ref string) (io.ReadCloser, error)

	// RemoveImage removes a local image. If force is true, running containers
	// referencing the image are also stopped and removed.
	RemoveImage(ctx context.Context, id string, force bool) error

	// Close releases any resources held by the engine client.
	Close() error
}
