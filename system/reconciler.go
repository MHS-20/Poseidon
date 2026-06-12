package system

import (
	"context"
	"log"
	"time"
)

type Agent struct {
	cm       *ContainerManager
	reader   *ManifestReader
	interval time.Duration
}

func NewAgent(manifestDir string, interval time.Duration) (*Agent, error) {
	cm, err := NewContainerManager()
	if err != nil {
		return nil, err
	}
	return &Agent{
		cm:       cm,
		reader:   NewManifestReader(manifestDir),
		interval: interval,
	}, nil
}

func (a *Agent) Run(ctx context.Context) error {
	log.Printf("System agent started (manifests=%s, interval=%s)", a.reader.Dir, a.interval)
	for {
		select {
		case <-ctx.Done():
			log.Println("System agent shutting down")
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			a.cm.StopAll(stopCtx)
			cancel()
			return ctx.Err()
		default:
		}

		a.reconcile(ctx)
		time.Sleep(a.interval)
	}
}

func (a *Agent) reconcile(ctx context.Context) {
	manifests, err := a.reader.ReadAll()
	if err != nil {
		log.Printf("error reading manifests: %v", err)
		return
	}

	for _, m := range manifests {
		if err := a.cm.EnsureRunning(ctx, m); err != nil {
			log.Printf("error ensuring %s: %v", m.Name, err)
		}
	}
}
