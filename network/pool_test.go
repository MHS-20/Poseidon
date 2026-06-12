package network

import (
	"testing"

	"github.com/MHS-20/Zodiac/test"
	"github.com/google/uuid"
)

func newTestPool(t *testing.T, cidr string) *Pool {
	t.Helper()
	h := test.NewHarness(t, 3)
	t.Cleanup(h.Shutdown)
	_ = h.CheckSingleLeader()
	kvCli := h.NewClient()
	pool, err := NewPool(kvCli, cidr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return pool
}

func TestVIPPoolAllocate(t *testing.T) {
	pool := newTestPool(t, "10.42.0.0/16")

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
}

func TestVIPPoolAllocateDuplicateReturnsSame(t *testing.T) {
	pool := newTestPool(t, "10.42.0.0/16")

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
}

func TestVIPPoolSequentialAllocation(t *testing.T) {
	pool := newTestPool(t, "10.42.0.0/16")

	id1 := uuid.New()
	id2 := uuid.New()
	ip1, err := pool.Allocate(id1)
	if err != nil {
		t.Fatalf("Allocate id1: %v", err)
	}
	ip2, err := pool.Allocate(id2)
	if err != nil {
		t.Fatalf("Allocate id2: %v", err)
	}
	if ip1.String() == ip2.String() {
		t.Errorf("sequential allocs returned same IP %s", ip1)
	}
	if ip1.String() != "10.42.0.1" {
		t.Errorf("first alloc got %s, want 10.42.0.1", ip1)
	}
	if ip2.String() != "10.42.0.2" {
		t.Errorf("second alloc got %s, want 10.42.0.2", ip2)
	}
}

func TestVIPPoolRelease(t *testing.T) {
	pool := newTestPool(t, "10.42.0.0/16")

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
}

func TestVIPPoolListAllocations(t *testing.T) {
	pool := newTestPool(t, "10.42.0.0/16")

	taskID := uuid.New()
	_, err := pool.Allocate(taskID)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	allocations, err := pool.ListAllocations()
	if err != nil {
		t.Fatalf("ListAllocations: %v", err)
	}
	if len(allocations) == 0 {
		t.Fatal("no allocations found")
	}
}

func TestVIPPoolCIDRValidation(t *testing.T) {
	_, err := NewPool(nil, "10.42.0.0/31")
	if err == nil {
		t.Error("expected error for /31 CIDR")
	}
}
