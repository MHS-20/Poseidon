package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MHS-20/Zodiac/test"
	pdns "github.com/MHS-20/poseidon/dns"
	"github.com/MHS-20/poseidon/manager"
	"github.com/MHS-20/poseidon/network"
	"github.com/MHS-20/poseidon/registry"
	"github.com/MHS-20/poseidon/store"
	"github.com/MHS-20/poseidon/task"
	"github.com/google/uuid"
	"github.com/miekg/dns"
)

// ---------------------------------------------------------------------------
// mockWorker — minimal worker HTTP server for testing
// ---------------------------------------------------------------------------

type mockWorker struct {
	mu     sync.Mutex
	server *httptest.Server
	tasks  []*task.Task
}

func newMockWorker() *mockWorker {
	mw := &mockWorker{}
	mw.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw.mu.Lock()
		defer mw.mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			var te task.TaskEvent
			if err := json.NewDecoder(r.Body).Decode(&te); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			te.Task.State = task.Running
			mw.tasks = append(mw.tasks, &te.Task)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(te.Task)

		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(mw.tasks)

		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "not implemented", http.StatusNotImplemented)
		}
	}))
	return mw
}

func (m *mockWorker) URL() string {
	return strings.TrimPrefix(m.server.URL, "http://")
}

func (m *mockWorker) Close() {
	m.server.Close()
}

// ---------------------------------------------------------------------------
// E2E test suite
// ---------------------------------------------------------------------------

func TestE2E(t *testing.T) {
	// Shared 3-node Zodiac cluster for subtests that need distributed KV.
	h := test.NewHarness(t, 3)
	defer h.Shutdown()

	_ = h.CheckSingleLeader()
	kvAll := h.NewClient()

	t.Run("RaftStoreBasic", func(t *testing.T) {
		ts := store.NewRaftTaskStore(kvAll)
		es := store.NewRaftEventStore(kvAll)

		taskID := uuid.New()
		now := time.Now().UTC()

		t1 := &task.Task{
			ID:    taskID,
			Name:  "test-task-1",
			Image: "nginx:latest",
			State: task.Pending,
			Cpu:   0.5,
			Memory: 256,
		}

		if err := ts.Put(taskID.String(), t1); err != nil {
			t.Fatalf("TaskStore.Put: %v", err)
		}
		t.Logf("Put task %s", taskID.String())

		got, err := ts.Get(taskID.String())
		if err != nil {
			t.Fatalf("TaskStore.Get: %v", err)
		}
		gotTask := got.(*task.Task)
		if gotTask.Name != "test-task-1" {
			t.Errorf("got name=%q, want %q", gotTask.Name, "test-task-1")
		}
		if gotTask.State != task.Pending {
			t.Errorf("got state=%v, want Pending", gotTask.State)
		}

		eventID := uuid.New()
		ev := &task.TaskEvent{
			ID:        eventID,
			State:     task.Pending,
			Timestamp: now,
			Task:      *t1,
		}
		if err := es.Put(eventID.String(), ev); err != nil {
			t.Fatalf("EventStore.Put: %v", err)
		}
		gotEv, err := es.Get(eventID.String())
		if err != nil {
			t.Fatalf("EventStore.Get: %v", err)
		}
		if gotEv.(*task.TaskEvent).ID != eventID {
			t.Errorf("event ID mismatch")
		}

		list, err := ts.List()
		if err != nil {
			t.Fatalf("TaskStore.List: %v", err)
		}
		tasks := list.([]*task.Task)
		found := false
		for _, tt := range tasks {
			if tt.ID == taskID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("task %s not found in List()", taskID.String())
		}

		count, err := ts.Count()
		if err != nil {
			t.Fatalf("TaskStore.Count: %v", err)
		}
		if count < 1 {
			t.Errorf("Count() = %d, want >= 1", count)
		}
	})

	t.Run("ManagerRoundTrip", func(t *testing.T) {
		mw := newMockWorker()
		defer mw.Close()

		m := manager.New(
			[]string{mw.URL()},
			"roundrobin",
			"raft",
			kvAll,
			func() bool { return true },
			nil,
		)

		taskID := uuid.New()
		te := task.TaskEvent{
			ID:        uuid.New(),
			State:     task.Scheduled,
			Timestamp: time.Now().UTC(),
			Task: task.Task{
				ID:    taskID,
				Name:  "e2e-test",
				Image: "alpine:latest",
				State: task.Scheduled,
				Cpu:   0.1,
			},
		}
		m.AddTask(te)
		t.Logf("Added pending task %s", taskID.String())

		time.Sleep(200 * time.Millisecond)

		m.SendWork()
		t.Log("SendWork completed")

		tasks := m.GetTasks()
		found := false
		for _, tt := range tasks {
			if tt.ID == taskID {
				found = true
				if tt.State != task.Scheduled && tt.State != task.Running {
					t.Errorf("task state = %v, want Scheduled or Running", tt.State)
				}
				break
			}
		}
		if !found {
			t.Errorf("task %s not found in GetTasks after SendWork", taskID.String())
		}
	})

	t.Run("LeaderFailover", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		h2 := test.NewHarness(t, 3)
		defer h2.Shutdown()

		initialLeader := h2.CheckSingleLeader()
		t.Logf("Current leader: node %d", initialLeader)

		kvCli := h2.NewClient()

		testKey := "/e2e/failover/pre-crash"
		_, _, _, err := kvCli.Put(ctx, testKey, "before-crash")
		if err != nil {
			t.Fatalf("Put before crash: %v", err)
		}
		t.Log("Data written before crash")

		h2.CrashService(initialLeader)
		t.Logf("Crashed leader node %d", initialLeader)

		time.Sleep(800 * time.Millisecond)
		newLeader := h2.CheckSingleLeader()
		t.Logf("New leader: node %d", newLeader)
		if newLeader == initialLeader {
			t.Errorf("new leader should differ from crashed leader")
		}

		// Must create a new client after the crash — the old kvCli still has
		// the dead node's address and the kvclient won't retry on connection
		// refused.
		kvLive := h2.NewClient()

		_, _, _, err = kvLive.Put(ctx, "/e2e/failover/post-crash", "after-crash")
		if err != nil {
			t.Fatalf("Put after crash: %v", err)
		}
		t.Log("Data written after new leader elected")

		val, found, _, err := kvLive.Get(ctx, testKey)
		if err != nil {
			t.Fatalf("Get after crash: %v", err)
		}
		if !found {
			t.Errorf("pre-crash key not found on new leader")
		} else if val != "before-crash" {
			t.Errorf("pre-crash value = %q, want %q", val, "before-crash")
		} else {
			t.Log("Pre-crash data survives leader change")
		}
	})

	t.Run("PendingTasksSurviveLeaderChange", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		h3 := test.NewHarness(t, 3)
		defer h3.Shutdown()

		leaderID := h3.CheckSingleLeader()
		t.Logf("Cluster formed, leader: node %d", leaderID)

		kvCli := h3.NewClient()

		teID := uuid.New().String()
		te := task.TaskEvent{
			ID:        uuid.MustParse(teID),
			State:     task.Scheduled,
			Timestamp: time.Now().UTC(),
			Task: task.Task{
				ID:    uuid.New(),
				Name:  "failover-test",
				Image: "alpine:latest",
				State: task.Scheduled,
			},
		}
		data, _ := json.Marshal(te)
		_, _, _, err := kvCli.Put(ctx, "/poseidon/tasks/pending/"+teID, string(data))
		if err != nil {
			t.Fatalf("Put pending task: %v", err)
		}
		t.Logf("Pending task written via leader %d", leaderID)

		h3.CrashService(leaderID)
		time.Sleep(800 * time.Millisecond)

		newLeader := h3.CheckSingleLeader()
		t.Logf("New leader: node %d", newLeader)
		if newLeader == leaderID {
			t.Fatal("new leader should differ from crashed leader")
		}

		kvNewLeader := h3.NewClient()
		pairs, _, err := kvNewLeader.List(ctx, "/poseidon/tasks/pending/")
		if err != nil {
			t.Fatalf("List pending tasks on new leader: %v", err)
		}
		if len(pairs) == 0 {
			t.Errorf("no pending tasks visible after leader change")
		} else {
			t.Logf("New leader sees %d pending task(s)", len(pairs))
		}
	})

	t.Run("VIPPoolAllocation", func(t *testing.T) {
		kvCli := h.NewClient()
		pool, err := network.NewPool(kvCli, "10.42.0.0/24")
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}

		taskID := uuid.New()
		ip, err := pool.Allocate(taskID)
		if err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if ip == nil || ip.String() != "10.42.0.1" {
			t.Errorf("got IP %v, want 10.42.0.1", ip)
		}

		got, err := pool.GetVIP(taskID)
		if err != nil {
			t.Fatalf("GetVIP: %v", err)
		}
		if got.String() != ip.String() {
			t.Errorf("GetVIP = %s, want %s", got, ip)
		}

		allocations, err := pool.ListAllocations()
		if err != nil {
			t.Fatalf("ListAllocations: %v", err)
		}
		found := false
		for _, v := range allocations {
			if v == taskID.String() {
				found = true
				break
			}
		}
		if !found {
			t.Error("task not found in allocations")
		}

		if err := pool.Release(taskID); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})

	t.Run("DNSServiceDiscovery", func(t *testing.T) {
		kvCli := h.NewClient()
		pool, err := network.NewPool(kvCli, "10.42.0.0/24")
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		reg := registry.New(kvCli)

		taskID := uuid.New()
		ip, err := pool.Allocate(taskID)
		if err != nil {
			t.Fatalf("pool allocate: %v", err)
		}

		inst := registry.ServiceInstance{
			ID:         taskID,
			Name:       "my-service",
			VirtualIP:  ip.String(),
			Ports:      []task.PortMapping{{ServicePort: 80, ContainerPort: 8080, Protocol: "tcp"}},
			Worker:     "10.0.0.1:5556",
			WorkerHost: "10.0.0.1",
			Status:     "Running",
			Healthy:    true,
		}
		if err := reg.Register(inst); err != nil {
			t.Fatalf("register: %v", err)
		}

		dnsSrv := pdns.New(kvCli)
		dnsAddr := "127.0.0.1:0"
		lis, err := net.ListenPacket("udp", dnsAddr)
		if err != nil {
			t.Fatalf("listen for DNS: %v", err)
		}
		realAddr := lis.LocalAddr().String()
		lis.Close()

		go func() {
			dnsSrv.Start(realAddr)
		}()
		time.Sleep(100 * time.Millisecond)
		defer dnsSrv.Stop()

		client := new(dns.Client)

		// A record
		t.Run("A", func(t *testing.T) {
			m := new(dns.Msg)
			m.SetQuestion("my-service.svc.poseidon.cluster.", dns.TypeA)
			resp, _, err := client.Exchange(m, realAddr)
			if err != nil {
				t.Fatalf("DNS exchange: %v", err)
			}
			if resp.Rcode != dns.RcodeSuccess {
				t.Fatalf("rcode = %v, want Success", resp.Rcode)
			}
			if len(resp.Answer) == 0 {
				t.Fatal("no answer")
			}
			a, ok := resp.Answer[0].(*dns.A)
			if !ok {
				t.Fatalf("answer type = %T", resp.Answer[0])
			}
			if a.A.String() != ip.String() {
				t.Errorf("resolved to %s, want %s", a.A, ip)
			}
		})

		// SRV record
		t.Run("SRV", func(t *testing.T) {
			m := new(dns.Msg)
			m.SetQuestion("my-service.svc.poseidon.cluster.", dns.TypeSRV)
			resp, _, err := client.Exchange(m, realAddr)
			if err != nil {
				t.Fatalf("SRV exchange: %v", err)
			}
			if resp.Rcode != dns.RcodeSuccess {
				t.Fatalf("SRV rcode = %v, want Success", resp.Rcode)
			}
			if len(resp.Answer) == 0 {
				t.Fatal("no SRV answer")
			}
			srv, ok := resp.Answer[0].(*dns.SRV)
			if !ok {
				t.Fatalf("SRV answer type = %T", resp.Answer[0])
			}
			if srv.Port != 80 {
				t.Errorf("SRV port = %d, want 80", srv.Port)
			}
		})

		// PTR record
		t.Run("PTR", func(t *testing.T) {
			parts := strings.Split(ip.String(), ".")
			ptrName := fmt.Sprintf("%s.%s.%s.%s.in-addr.arpa.", parts[3], parts[2], parts[1], parts[0])
			m := new(dns.Msg)
			m.SetQuestion(ptrName, dns.TypePTR)
			resp, _, err := client.Exchange(m, realAddr)
			if err != nil {
				t.Fatalf("PTR exchange: %v", err)
			}
			if resp.Rcode != dns.RcodeSuccess {
				t.Fatalf("PTR rcode = %v, want Success", resp.Rcode)
			}
			if len(resp.Answer) == 0 {
				t.Fatal("no PTR answer")
			}
			ptr, ok := resp.Answer[0].(*dns.PTR)
			if !ok {
				t.Fatalf("PTR answer type = %T", resp.Answer[0])
			}
			if ptr.Ptr != "my-service.svc.poseidon.cluster." {
				t.Errorf("PTR = %s, want my-service.svc.poseidon.cluster.", ptr.Ptr)
			}
		})
	})

	t.Run("RegistryServiceDiscovery", func(t *testing.T) {
		kvCli := h.NewClient()
		reg := registry.New(kvCli)

		id1 := uuid.New()
		web1 := registry.ServiceInstance{
			ID:         id1,
			Name:       "web",
			VirtualIP:  "10.42.0.10",
			Ports:      []task.PortMapping{{ServicePort: 80, ContainerPort: 8080, Protocol: "tcp"}},
			Worker:     "10.0.0.1:5556",
			WorkerHost: "10.0.0.1",
			Status:     "Running",
			Healthy:    true,
		}
		if err := reg.Register(web1); err != nil {
			t.Fatalf("register web1: %v", err)
		}

		id2 := uuid.New()
		web2 := registry.ServiceInstance{
			ID:         id2,
			Name:       "web",
			VirtualIP:  "10.42.0.11",
			Ports:      []task.PortMapping{{ServicePort: 80, ContainerPort: 8080, Protocol: "tcp"}},
			Worker:     "10.0.0.2:5556",
			WorkerHost: "10.0.0.2",
			Status:     "Running",
			Healthy:    true,
		}
		if err := reg.Register(web2); err != nil {
			t.Fatalf("register web2: %v", err)
		}

		instances, err := reg.Lookup("web")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if len(instances) != 2 {
			t.Fatalf("got %d instances, want 2", len(instances))
		}

		healthy, err := reg.LookupHealthy("web")
		if err != nil {
			t.Fatalf("LookupHealthy: %v", err)
		}
		if len(healthy) != 2 {
			t.Errorf("got %d healthy, want 2", len(healthy))
		}

		reg.SetHealth(id1, false)

		healthy, err = reg.LookupHealthy("web")
		if err != nil {
			t.Fatalf("LookupHealthy after unhealthy: %v", err)
		}
		if len(healthy) != 1 {
			t.Errorf("got %d healthy after set unhealthy, want 1", len(healthy))
		}

		services, err := reg.ListServices()
		if err != nil {
			t.Fatalf("ListServices: %v", err)
		}
		if _, ok := services["web"]; !ok {
			t.Error("web service not found in ListServices")
		}

		reg.Deregister(id2)
		instances, _ = reg.Lookup("web")
		if len(instances) != 1 {
			t.Errorf("got %d instances after deregister, want 1", len(instances))
		}
	})
}
