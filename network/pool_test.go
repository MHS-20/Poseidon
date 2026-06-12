package network

import (
	"testing"

	"github.com/MHS-20/Zodiac/test"
	"github.com/google/uuid"
)

func TestVIPPool(t *testing.T) {
	h := test.NewHarness(t, 3)
	defer h.Shutdown()

	_ = h.CheckSingleLeader()
	kvCli := h.NewClient()

	pool, err := NewPool(kvCli, "10.42.0.0/16")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	t.Run("Allocate", func(t *testing.T) {
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
	})

	t.Run("AllocateDuplicateReturnsSame", func(t *testing.T) {
		taskID := uuid.New()
		ip1, err := pool.Allocate(taskID)
		if err != nil {
			t.Fatalf("first Allocate: %v", err)
		}
		ip2, err := pool.Allocate(taskID)
		if err != nil {
			t.Fatalf("second Allocate: %v", err)
		}
		if ip1.String() != ip2.String() {
			t.Errorf("duplicate allocate: got %s, want %s", ip2, ip1)
		}
	})

	t.Run("SequentialAllocation", func(t *testing.T) {
		id1 := uuid.New()
		id2 := uuid.New()
		ip1, _ := pool.Allocate(id1)
		ip2, _ := pool.Allocate(id2)
		if ip1.String() == ip2.String() {
			t.Errorf("sequential allocs returned same IP %s", ip1)
		}
	})

	t.Run("Release", func(t *testing.T) {
		taskID := uuid.New()
		ip, err := pool.Allocate(taskID)
		if err != nil {
			t.Fatalf("Allocate before release: %v", err)
		}
		if err := pool.Release(taskID); err != nil {
			t.Fatalf("Release: %v", err)
		}
		_, err = pool.GetVIP(taskID)
		if err == nil {
			t.Error("GetVIP after release should fail")
		}
		_ = ip
	})

	t.Run("ListAllocations", func(t *testing.T) {
		taskID := uuid.New()
		pool.Allocate(taskID)
		allocations, err := pool.ListAllocations()
		if err != nil {
			t.Fatalf("ListAllocations: %v", err)
		}
		ipStr := allocations["/poseidon/vips/ips/"]
		if ipStr != "" {
			found := false
			for k, v := range allocations {
				if v == taskID.String() {
					found = true
					break
				}
				_ = k
			}
			if !found {
				t.Errorf("task %s not found in allocations", taskID.String())
			}
		}
		_ = ipStr
	})
}

func TestVIPPoolCIDRValidation(t *testing.T) {
	h := test.NewHarness(t, 3)
	defer h.Shutdown()
	_ = h.CheckSingleLeader()
	kvCli := h.NewClient()

	_, err := NewPool(kvCli, "10.42.0.0/31")
	if err == nil {
		t.Error("expected error for /31 CIDR")
	}
}
