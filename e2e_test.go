package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MHS-20/Zodiac/test"
	"github.com/MHS-20/poseidon/manager"
	"github.com/MHS-20/poseidon/store"
	"github.com/MHS-20/poseidon/task"
	"github.com/google/uuid"
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
}
