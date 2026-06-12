package system

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type ContainerManager struct {
	cli    *client.Client
	names  map[string]string
}

func NewContainerManager() (*ContainerManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &ContainerManager{
		cli:   cli,
		names: make(map[string]string),
	}, nil
}

func (cm *ContainerManager) EnsureRunning(ctx context.Context, m Manifest) error {
	cid, ok := cm.names[m.Name]
	if ok {
		ok, err := cm.isRunning(ctx, cid)
		if err != nil {
			return fmt.Errorf("check %s: %w", m.Name, err)
		}
		if ok {
			return nil
		}
		log.Printf("Container %s is not running, restarting", m.Name)
		if err := cm.remove(ctx, cid); err != nil {
			log.Printf("remove stale container %s: %v", m.Name, err)
		}
		delete(cm.names, m.Name)
	}

	if err := cm.ensureImage(ctx, m.Image); err != nil {
		return fmt.Errorf("ensure image %s: %w", m.Image, err)
	}

	portSet, portMap := buildPorts(m.Ports)

	env := make([]string, 0, len(m.Env))
	for k, v := range m.Env {
		env = append(env, k+"="+v)
	}

	cc := container.Config{
		Image:        m.Image,
		Cmd:          m.Cmd,
		Env:          env,
		ExposedPorts: portSet,
	}
	hc := container.HostConfig{
		PortBindings: portMap,
		Resources: container.Resources{
			Memory:   m.Memory,
			NanoCPUs: int64(m.CPUs * 1e9),
		},
	}
	if m.NetworkMode != "" {
		hc.NetworkMode = container.NetworkMode(m.NetworkMode)
	}
	if m.RestartPolicy == "always" {
		hc.RestartPolicy.Name = "always"
	}

	resp, err := cm.cli.ContainerCreate(ctx, &cc, &hc, nil, nil, m.Name)
	if err != nil {
		return fmt.Errorf("create container %s: %w", m.Name, err)
	}

	if err := cm.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", m.Name, err)
	}

	cm.names[m.Name] = resp.ID
	log.Printf("Started system container %s (id=%s)", m.Name, resp.ID[:12])
	return nil
}

func (cm *ContainerManager) StopAll(ctx context.Context) {
	for name, cid := range cm.names {
		log.Printf("Stopping system container %s", name)
		if err := cm.stop(ctx, cid); err != nil {
			log.Printf("error stopping %s: %v", name, err)
		}
	}
}

func (cm *ContainerManager) isRunning(ctx context.Context, cid string) (bool, error) {
	info, err := cm.cli.ContainerInspect(ctx, cid)
	if err != nil {
		return false, nil
	}
	return info.State.Running, nil
}

func (cm *ContainerManager) remove(ctx context.Context, cid string) error {
	return cm.cli.ContainerRemove(ctx, cid, types.ContainerRemoveOptions{Force: true})
}

func (cm *ContainerManager) stop(ctx context.Context, cid string) error {
	timeout := 10 * time.Second
	return cm.cli.ContainerStop(ctx, cid, &timeout)
}

func (cm *ContainerManager) ensureImage(ctx context.Context, image string) error {
	_, _, err := cm.cli.ImageInspectWithRaw(ctx, image)
	if err == nil {
		return nil
	}
	log.Printf("Pulling image %s", image)
	reader, err := cm.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	defer reader.Close()
	return nil
}

func buildPorts(ports []ManifestPort) (nat.PortSet, nat.PortMap) {
	ps := make(nat.PortSet)
	pm := make(nat.PortMap)
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		np, _ := nat.NewPort(proto, fmt.Sprint(p.ContainerPort))
		ps[np] = struct{}{}
		if p.HostPort > 0 {
			pm[np] = []nat.PortBinding{{HostPort: fmt.Sprint(p.HostPort)}}
		}
	}
	return ps, pm
}
