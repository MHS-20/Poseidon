package network

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net"
	"strconv"
	"time"

	"github.com/MHS-20/Zodiac/api"
	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/google/uuid"
)

const (
	kVIPNextKey  = "/poseidon/vips/next"
	kVIPAllocKey = "/poseidon/vips/allocations/"
	kVIPTaskKey  = "/poseidon/vips/ips/"
)

type Pool struct {
	client  *kvclient.KVClient
	network *net.IPNet
	base    net.IP
	size    int
}

func NewPool(client *kvclient.KVClient, cidr string) (*Pool, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ones, bits := ipnet.Mask.Size()
	size := 1 << (bits - ones)
	if size < 3 {
		return nil, fmt.Errorf("CIDR %q too small (need at least 2 usable addresses)", cidr)
	}
	return &Pool{
		client:  client,
		network: ipnet,
		base:    ipnet.IP.Mask(ipnet.Mask),
		size:    size,
	}, nil
}

func (p *Pool) indexToIP(idx int) net.IP {
	n := big.NewInt(0).SetBytes(p.base)
	n.Add(n, big.NewInt(int64(idx)))
	ip := n.Bytes()
	if len(ip) < 4 {
		ip = append(make(net.IP, 4-len(ip)), ip...)
	}
	return ip
}

func (p *Pool) allocateIndex(ctx context.Context) (int, error) {
	for {
		val, found, _, err := p.client.Get(ctx, kVIPNextKey)
		var current int
		if err != nil {
			return 0, fmt.Errorf("get next index: %w", err)
		}
		if !found {
			current = 1
		} else {
			current, err = strconv.Atoi(val)
			if err != nil {
				return 0, fmt.Errorf("parse next index: %w", err)
			}
		}
		if current >= p.size {
			return 0, fmt.Errorf("VIP pool exhausted (CIDR %s)", p.network.String())
		}
		nextVal := strconv.Itoa(current + 1)
		_, _, _, err = p.client.CAS(ctx, kVIPNextKey, val, nextVal)
		if err != nil {
			continue
		}
		return current, nil
	}
}

func (p *Pool) Allocate(taskID uuid.UUID) (net.IP, error) {
	ctx := context.Background()

	existing, _, _, err := p.client.Get(ctx, kVIPAllocKey+taskID.String())
	if err == nil && existing != "" {
		return net.ParseIP(existing), nil
	}

	idx, err := p.allocateIndex(ctx)
	if err != nil {
		return nil, err
	}

	ip := p.indexToIP(idx)
	ipStr := ip.String()

	_, _, _, err = p.client.Put(ctx, kVIPAllocKey+taskID.String(), ipStr)
	if err != nil {
		return nil, fmt.Errorf("store allocation: %w", err)
	}
	_, _, _, err = p.client.Put(ctx, kVIPTaskKey+ipStr, taskID.String())
	if err != nil {
		return nil, fmt.Errorf("store ip mapping: %w", err)
	}

	log.Printf("Allocated VIP %s to task %s", ipStr, taskID.String())
	return ip, nil
}

func (p *Pool) Release(taskID uuid.UUID) error {
	ctx := context.Background()
	ipStr, _, _, err := p.client.Get(ctx, kVIPAllocKey+taskID.String())
	if err != nil {
		return err
	}
	if ipStr == "" {
		return nil
	}
	_, _, _, err = p.client.Txn(ctx,
		[]api.TxnCondition{
			{Key: kVIPAllocKey + taskID.String(), Compare: api.CompareExists},
		},
		[]api.TxnOp{
			{Op: api.TxnOpDelete, Key: kVIPAllocKey + taskID.String()},
			{Op: api.TxnOpDelete, Key: kVIPTaskKey + ipStr},
		},
		nil,
	)
	if err != nil {
		return fmt.Errorf("release vip: %w", err)
	}
	log.Printf("Released VIP %s for task %s", ipStr, taskID.String())
	return nil
}

func (p *Pool) GetVIP(taskID uuid.UUID) (net.IP, error) {
	ctx := context.Background()
	ipStr, _, _, err := p.client.Get(ctx, kVIPAllocKey+taskID.String())
	if err != nil {
		return nil, err
	}
	if ipStr == "" {
		return nil, fmt.Errorf("no VIP allocated for task %s", taskID.String())
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("invalid VIP %q for task %s", ipStr, taskID.String())
	}
	return ip, nil
}

func (p *Pool) ListAllocations() (map[string]string, error) {
	ctx := context.Background()
	pairs, _, err := p.client.List(ctx, kVIPTaskKey)
	if err != nil {
		return nil, err
	}
	return pairs, nil
}

type AllocationEvent struct {
	IP     string
	TaskID string
	Action string
}

func (p *Pool) WatchAllocations(ctx context.Context, ch chan<- AllocationEvent) {
	seen := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pairs, _, err := p.client.List(ctx, kVIPTaskKey)
		if err != nil {
			log.Printf("pool watch list error: %v", err)
			goto sleep
		}
		for ip, taskID := range pairs {
			if prev, ok := seen[ip]; !ok {
				seen[ip] = taskID
				select {
				case ch <- AllocationEvent{IP: ip, TaskID: taskID, Action: "add"}:
				default:
				}
			} else if prev != taskID {
				seen[ip] = taskID
				select {
				case ch <- AllocationEvent{IP: ip, TaskID: taskID, Action: "update"}:
				default:
				}
			}
		}
		for ip := range seen {
			if _, ok := pairs[ip]; !ok {
				delete(seen, ip)
				select {
				case ch <- AllocationEvent{IP: ip, TaskID: "", Action: "remove"}:
				default:
				}
			}
		}
	sleep:
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
