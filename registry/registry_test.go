package registry

import (
	"context"
	"testing"
	"time"

	"github.com/MHS-20/Zodiac/test"
	"github.com/MHS-20/poseidon/task"
	"github.com/google/uuid"
)

func TestRegistry(t *testing.T) {
	h := test.NewHarness(t, 3)
	defer h.Shutdown()

	_ = h.CheckSingleLeader()
	kvCli := h.NewClient()
	reg := New(kvCli)

	t.Run("RegisterAndLookup", func(t *testing.T) {
		id := uuid.New()
		inst := ServiceInstance{
			ID:         id,
			Name:       "web",
			VirtualIP:  "10.42.0.1",
			Ports:      []task.PortMapping{{ServicePort: 80, ContainerPort: 8080, Protocol: "tcp"}},
			Worker:     "10.0.0.1:5556",
			WorkerHost: "10.0.0.1",
			Status:     "Running",
			Healthy:    true,
		}
		if err := reg.Register(inst); err != nil {
			t.Fatalf("Register: %v", err)
		}

		instances, err := reg.Lookup("web")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if len(instances) != 1 {
			t.Fatalf("got %d instances, want 1", len(instances))
		}
		if instances[0].VirtualIP != "10.42.0.1" {
			t.Errorf("VIP = %s, want 10.42.0.1", instances[0].VirtualIP)
		}
	})

	t.Run("LookupHealthy", func(t *testing.T) {
		healthy, err := reg.LookupHealthy("web")
		if err != nil {
			t.Fatalf("LookupHealthy: %v", err)
		}
		if len(healthy) != 1 {
			t.Errorf("got %d healthy, want 1", len(healthy))
		}
	})

	t.Run("UnhealthyFiltered", func(t *testing.T) {
		id := uuid.New()
		inst := ServiceInstance{
			ID:        id,
			Name:      "db",
			VirtualIP: "10.42.0.2",
			Status:    "Running",
			Healthy:   false,
		}
		reg.Register(inst)

		healthy, err := reg.LookupHealthy("db")
		if err != nil {
			t.Fatalf("LookupHealthy: %v", err)
		}
		if len(healthy) != 0 {
			t.Errorf("got %d healthy, want 0 (unhealthy)", len(healthy))
		}

		all, err := reg.Lookup("db")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("got %d instances, want 1", len(all))
		}
	})

	t.Run("SetHealth", func(t *testing.T) {
		id := uuid.New()
		inst := ServiceInstance{
			ID:        id,
			Name:      "health-test",
			VirtualIP: "10.42.0.3",
			Status:    "Running",
			Healthy:   false,
		}
		reg.Register(inst)

		if err := reg.SetHealth(id, true); err != nil {
			t.Fatalf("SetHealth: %v", err)
		}

		updated, found, err := reg.LookupByID(id)
		if err != nil {
			t.Fatalf("LookupByID: %v", err)
		}
		if !found {
			t.Fatal("instance not found")
		}
		if !updated.Healthy {
			t.Error("instance should be healthy after SetHealth(true)")
		}
	})

	t.Run("Deregister", func(t *testing.T) {
		id := uuid.New()
		inst := ServiceInstance{
			ID:        id,
			Name:      "temp",
			VirtualIP: "10.42.0.4",
			Status:    "Running",
			Healthy:   true,
		}
		reg.Register(inst)

		if err := reg.Deregister(id); err != nil {
			t.Fatalf("Deregister: %v", err)
		}

		_, found, err := reg.LookupByID(id)
		if err != nil {
			t.Fatalf("LookupByID after deregister: %v", err)
		}
		if found {
			t.Error("instance should not be found after deregister")
		}
	})

	t.Run("ListServices", func(t *testing.T) {
		services, err := reg.ListServices()
		if err != nil {
			t.Fatalf("ListServices: %v", err)
		}
		if _, ok := services["web"]; !ok {
			t.Error("web service not found in ListServices")
		}
		if _, ok := services["db"]; !ok {
			t.Error("db service not found in ListServices")
		}
	})
}

func TestRegistryWatch(t *testing.T) {
	h := test.NewHarness(t, 3)
	defer h.Shutdown()

	_ = h.CheckSingleLeader()
	kvCli := h.NewClient()
	reg := NewWithPeriod(kvCli, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan RegistryEvent, 10)
	go reg.WatchAll(ctx, ch)

	time.Sleep(600 * time.Millisecond)

	id := uuid.New()
	inst := ServiceInstance{
		ID:        id,
		Name:      "watch-test",
		VirtualIP: "10.42.0.10",
		Status:    "Running",
		Healthy:   true,
	}
	reg.Register(inst)

	select {
	case ev := <-ch:
		if ev.Action != "add" {
			t.Errorf("action = %s, want add", ev.Action)
		}
		if ev.Instance.Name != "watch-test" {
			t.Errorf("name = %s, want watch-test", ev.Instance.Name)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}
