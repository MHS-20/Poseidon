package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/MHS-20/Zodiac/api"
	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/poseidon/task"
	"github.com/google/uuid"
)

const (
	kServicePrefix  = "/poseidon/registry/services/"
	kInstancePrefix = "/poseidon/registry/instances/"
)

type ServiceInstance struct {
	ID        uuid.UUID          `json:"id"`
	Name      string             `json:"name"`
	VirtualIP string             `json:"virtualIP"`
	Ports     []task.PortMapping `json:"ports,omitempty"`
	Worker    string             `json:"worker"`
	WorkerHost string            `json:"workerHost"`
	Status    string             `json:"status"`
	Healthy   bool               `json:"healthy"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

type RegistryEvent struct {
	Instance ServiceInstance
	Action   string
}

type Registry struct {
	client      *kvclient.KVClient
	watchPeriod time.Duration
}

func New(client *kvclient.KVClient) *Registry {
	return &Registry{
		client:      client,
		watchPeriod: 10 * time.Second,
	}
}

func NewWithPeriod(client *kvclient.KVClient, period time.Duration) *Registry {
	return &Registry{
		client:      client,
		watchPeriod: period,
	}
}

func (r *Registry) Register(inst ServiceInstance) error {
	ctx := context.Background()
	inst.UpdatedAt = time.Now().UTC()

	serviceKey := kServicePrefix + inst.Name + "/" + inst.ID.String()
	data, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("marshal instance: %w", err)
	}

	_, _, _, err = r.client.Put(ctx, serviceKey, string(data))
	if err != nil {
		return fmt.Errorf("put service instance: %w", err)
	}

	_, _, _, err = r.client.Put(ctx, kInstancePrefix+inst.ID.String(), inst.Name)
	if err != nil {
		return fmt.Errorf("put instance index: %w", err)
	}

	log.Printf("Registry: registered %s/%s (VIP=%s, worker=%s)", inst.Name, inst.ID, inst.VirtualIP, inst.Worker)
	return nil
}

func (r *Registry) Deregister(instanceID uuid.UUID) error {
	ctx := context.Background()

	serviceName, _, _, err := r.client.Get(ctx, kInstancePrefix+instanceID.String())
	if err != nil {
		return fmt.Errorf("get instance index: %w", err)
	}

	if serviceName == "" {
		log.Printf("Registry: instance %s not found, skipping deregister", instanceID)
		return nil
	}

	serviceKey := kServicePrefix + serviceName + "/" + instanceID.String()
	_, _, _, err = r.client.Txn(ctx,
		[]api.TxnCondition{
			{Key: serviceKey, Compare: api.CompareExists},
		},
		[]api.TxnOp{
			{Op: api.TxnOpDelete, Key: serviceKey},
			{Op: api.TxnOpDelete, Key: kInstancePrefix + instanceID.String()},
		},
		nil,
	)
	if err != nil {
		return fmt.Errorf("deregister instance: %w", err)
	}

	log.Printf("Registry: deregistered %s/%s", serviceName, instanceID)
	return nil
}

func (r *Registry) Lookup(name string) ([]ServiceInstance, error) {
	ctx := context.Background()
	prefix := kServicePrefix + name + "/"
	pairs, _, err := r.client.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list service instances: %w", err)
	}

	instances := make([]ServiceInstance, 0, len(pairs))
	for _, data := range pairs {
		var inst ServiceInstance
		if err := json.Unmarshal([]byte(data), &inst); err != nil {
			log.Printf("Registry: error decoding instance: %v", err)
			continue
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

func (r *Registry) LookupHealthy(name string) ([]ServiceInstance, error) {
	instances, err := r.Lookup(name)
	if err != nil {
		return nil, err
	}
	healthy := make([]ServiceInstance, 0, len(instances))
	for _, inst := range instances {
		if inst.Healthy && inst.Status == "Running" {
			healthy = append(healthy, inst)
		}
	}
	return healthy, nil
}

func (r *Registry) LookupByID(instanceID uuid.UUID) (ServiceInstance, bool, error) {
	ctx := context.Background()
	serviceName, found, _, err := r.client.Get(ctx, kInstancePrefix+instanceID.String())
	if err != nil {
		return ServiceInstance{}, false, fmt.Errorf("get instance index: %w", err)
	}
	if !found {
		return ServiceInstance{}, false, nil
	}

	prefix := kServicePrefix + serviceName + "/" + instanceID.String()
	val, found, _, err := r.client.Get(ctx, prefix)
	if err != nil {
		return ServiceInstance{}, false, fmt.Errorf("get instance data: %w", err)
	}
	if !found {
		return ServiceInstance{}, false, nil
	}

	var inst ServiceInstance
	if err := json.Unmarshal([]byte(val), &inst); err != nil {
		return ServiceInstance{}, false, fmt.Errorf("decode instance: %w", err)
	}
	return inst, true, nil
}

func (r *Registry) ListServices() (map[string][]ServiceInstance, error) {
	ctx := context.Background()
	pairs, _, err := r.client.List(ctx, kServicePrefix)
	if err != nil {
		return nil, fmt.Errorf("list all services: %w", err)
	}

	result := make(map[string][]ServiceInstance)
	for key, data := range pairs {
		trimmed := strings.TrimPrefix(key, kServicePrefix)
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]

		var inst ServiceInstance
		if err := json.Unmarshal([]byte(data), &inst); err != nil {
			continue
		}
		result[serviceName] = append(result[serviceName], inst)
	}
	return result, nil
}

func (r *Registry) SetHealth(instanceID uuid.UUID, healthy bool) error {
	inst, found, err := r.LookupByID(instanceID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("instance %s not found", instanceID)
	}

	inst.Healthy = healthy
	inst.UpdatedAt = time.Now().UTC()
	return r.Register(inst)
}

func (r *Registry) WatchAll(ctx context.Context, ch chan<- RegistryEvent) {
	seen := make(map[string]string)
	var current map[string]string
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		services, err := r.ListServices()
		if err != nil {
			log.Printf("Registry: watch list error: %v", err)
			goto sleep
		}

		current = make(map[string]string)
		for name, instances := range services {
			for _, inst := range instances {
				key := kServicePrefix + name + "/" + inst.ID.String()
				data, _ := json.Marshal(inst)
				current[key] = string(data)
			}
		}

		for key, data := range current {
			if prev, ok := seen[key]; !ok {
				seen[key] = data
				var inst ServiceInstance
				json.Unmarshal([]byte(data), &inst)
				ch <- RegistryEvent{Instance: inst, Action: "add"}
			} else if prev != data {
				seen[key] = data
				var inst ServiceInstance
				json.Unmarshal([]byte(data), &inst)
				ch <- RegistryEvent{Instance: inst, Action: "update"}
			}
		}
		for key := range seen {
			if _, ok := current[key]; !ok {
				delete(seen, key)
				parts := strings.SplitN(strings.TrimPrefix(key, kServicePrefix), "/", 2)
				if len(parts) == 2 {
					id := uuid.MustParse(parts[1])
					ch <- RegistryEvent{
						Instance: ServiceInstance{ID: id, Name: parts[0]},
						Action:   "remove",
					}
				}
			}
		}

	sleep:
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.watchPeriod):
		}
	}
}
