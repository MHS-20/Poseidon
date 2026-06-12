package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/poseidon/task"
)

type RaftStore struct {
	client      *kvclient.KVClient
	prefix      string
	decode      func([]byte) (interface{}, error)
	decodeList  func(map[string]string) (interface{}, error)
}

func NewRaftTaskStore(client *kvclient.KVClient) *RaftStore {
	return &RaftStore{
		client: client,
		prefix: "/poseidon/tasks/",
		decode: func(b []byte) (interface{}, error) {
			var t task.Task
			err := json.Unmarshal(b, &t)
			return &t, err
		},
		decodeList: func(pairs map[string]string) (interface{}, error) {
			keys := sortedKeys(pairs)
			tasks := make([]*task.Task, 0, len(pairs))
			for _, k := range keys {
				var t task.Task
				if err := json.Unmarshal([]byte(pairs[k]), &t); err != nil {
					return nil, err
				}
				tasks = append(tasks, &t)
			}
			return tasks, nil
		},
	}
}

func NewRaftEventStore(client *kvclient.KVClient) *RaftStore {
	return &RaftStore{
		client: client,
		prefix: "/poseidon/events/",
		decode: func(b []byte) (interface{}, error) {
			var e task.TaskEvent
			err := json.Unmarshal(b, &e)
			return &e, err
		},
		decodeList: func(pairs map[string]string) (interface{}, error) {
			keys := sortedKeys(pairs)
			events := make([]*task.TaskEvent, 0, len(pairs))
			for _, k := range keys {
				var e task.TaskEvent
				if err := json.Unmarshal([]byte(pairs[k]), &e); err != nil {
					return nil, err
				}
				events = append(events, &e)
			}
			return events, nil
		},
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (r *RaftStore) Put(key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, _, _, err = r.client.Put(ctx, r.prefix+key, string(data))
	return err
}

func (r *RaftStore) Get(key string) (interface{}, error) {
	ctx := context.Background()
	val, found, _, err := r.client.Get(ctx, r.prefix+key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("key %s not found", key)
	}
	return r.decode([]byte(val))
}

func (r *RaftStore) List() (interface{}, error) {
	ctx := context.Background()
	pairs, _, err := r.client.List(ctx, r.prefix)
	if err != nil {
		return nil, err
	}
	return r.decodeList(pairs)
}

func (r *RaftStore) Count() (int, error) {
	ctx := context.Background()
	pairs, _, err := r.client.List(ctx, r.prefix)
	if err != nil {
		return -1, err
	}
	return len(pairs), nil
}
