package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// DockerEngine implements Engine using the Docker SDK.
type DockerEngine struct {
	cli *client.Client
}

// NewDockerEngine creates a new DockerEngine connected to the local Docker daemon.
func NewDockerEngine() (*DockerEngine, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerEngine{cli: cli}, nil
}

func (e *DockerEngine) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	list, err := e.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		out = append(out, toContainerInfo(c))
	}
	return out, nil
}

func (e *DockerEngine) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	resp, err := e.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", id, err)
	}
	info := &ContainerInfo{
		ID:     resp.ID[:min(12, len(resp.ID))],
		Name:   strings.TrimPrefix(resp.Name, "/"),
		Image:  resp.Config.Image,
		State:  resp.State.Status,
		Status: resp.State.Status,
		Labels: resp.Config.Labels,
		Stack:  resp.Config.Labels["dill.stack"],
	}
	return info, nil
}

func (e *DockerEngine) StartContainer(ctx context.Context, id string) error {
	return e.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (e *DockerEngine) StopContainer(ctx context.Context, id string) error {
	timeout := 10
	return e.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (e *DockerEngine) RestartContainer(ctx context.Context, id string) error {
	timeout := 10
	return e.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (e *DockerEngine) ContainerLogs(ctx context.Context, id string, follow bool, tail int) (io.ReadCloser, error) {
	tailStr := "all"
	if tail > 0 {
		tailStr = fmt.Sprintf("%d", tail)
	}
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tailStr,
		Timestamps: false,
	}
	return e.cli.ContainerLogs(ctx, id, opts)
}

func (e *DockerEngine) ListImages(ctx context.Context) ([]ImageInfo, error) {
	list, err := e.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	out := make([]ImageInfo, 0, len(list))
	for _, img := range list {
		id := img.ID
		if strings.HasPrefix(id, "sha256:") {
			id = id[7:min(19, len(id))]
		}
		out = append(out, ImageInfo{
			ID:      id,
			Tags:    img.RepoTags,
			Size:    img.Size,
			Created: time.Unix(img.Created, 0),
		})
	}
	return out, nil
}

func (e *DockerEngine) PullImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	return e.cli.ImagePull(ctx, ref, image.PullOptions{})
}

func (e *DockerEngine) RemoveImage(ctx context.Context, id string, force bool) error {
	_, err := e.cli.ImageRemove(ctx, id, image.RemoveOptions{Force: force, PruneChildren: false})
	return err
}

func (e *DockerEngine) Close() error {
	return e.cli.Close()
}

// toContainerInfo converts a Docker API container summary to our ContainerInfo type.
func toContainerInfo(c types.Container) ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	ports := make([]string, 0, len(c.Ports))
	for _, p := range c.Ports {
		if p.PublicPort != 0 {
			ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type))
		}
	}
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	return ContainerInfo{
		ID:     id,
		Name:   name,
		Image:  c.Image,
		State:  c.State,
		Status: c.Status,
		Ports:  ports,
		Labels: c.Labels,
		Stack:  c.Labels["dill.stack"],
	}
}

// stackContainers returns containers filtered by dill.stack label.
func stackContainers(ctx context.Context, cli *client.Client, stackName string) ([]ContainerInfo, error) {
	f := filters.NewArgs(filters.Arg("label", "dill.stack="+stackName))
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		out = append(out, toContainerInfo(c))
	}
	return out, nil
}

// StackContainers returns containers belonging to a dill stack.
func (e *DockerEngine) StackContainers(ctx context.Context, stackName string) ([]ContainerInfo, error) {
	return stackContainers(ctx, e.cli, stackName)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
