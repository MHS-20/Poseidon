package system

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Manifest struct {
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	RestartPolicy string            `json:"restartPolicy"`
	Ports         []ManifestPort    `json:"ports,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Volumes       []string          `json:"volumes,omitempty"`
	NetworkMode   string            `json:"networkMode,omitempty"`
	Cmd           []string          `json:"cmd,omitempty"`
	CPUs          float64           `json:"cpus,omitempty"`
	Memory        int64             `json:"memory,omitempty"`
}

type ManifestPort struct {
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type ManifestReader struct {
	Dir string
}

func NewManifestReader(dir string) *ManifestReader {
	return &ManifestReader{Dir: dir}
}

func (r *ManifestReader) ReadAll() ([]Manifest, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, fmt.Errorf("read manifests dir %s: %w", r.Dir, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var manifests []Manifest
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(r.Dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read manifest %s: %w", p, err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse manifest %s: %w", p, err)
		}
		if m.Name == "" || m.Image == "" {
			return nil, fmt.Errorf("manifest %s: name and image are required", p)
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}
