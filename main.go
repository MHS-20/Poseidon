package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/MHS-20/poseidon/manager"
	"github.com/MHS-20/poseidon/task"
	"github.com/MHS-20/poseidon/worker"
	"github.com/docker/docker/client"

	"github.com/golang-collections/collections/queue"
	"github.com/google/uuid"
)

func createContainer() (*task.Docker, *task.DockerResult) {
	c := task.Config{
		Name:  "test-container-1",
		Image: "postgres:13",
		Env: []string{
			"POSTGRES_USER=poseidon",
			"POSTGRES_PASSWORD=poseidon",
		},
	}

	dc, _ := client.NewClientWithOpts(client.FromEnv)
	d := task.Docker{
		Client: dc,
		Config: c,
	}

	result := d.Run()
	if result.Error != nil {
		fmt.Printf("%v\n", result.Error)
		return nil, nil
	}

	fmt.Printf(
		"Container %s is running with config %v\n", result.ContainerId, c)
	return &d, &result
}

func stopContainer(d *task.Docker, id string) *task.DockerResult {
	result := d.Stop(id)
	if result.Error != nil {
		fmt.Printf("%v\n", result.Error)
		return nil
	}

	fmt.Printf(
		"Container %s has been stopped and removed\n", result.ContainerId)
	return &result
}

func main() {
	whost := os.Getenv("POSEIDON_WORKER_HOST")
	wport, _ := strconv.Atoi(os.Getenv("POSEIDON_WORKER_PORT"))
	mhost := os.Getenv("POSEIDON_MANAGER_HOST")
	mport, _ := strconv.Atoi(os.Getenv("POSEIDON_MANAGER_PORT"))

	fmt.Println("Starting Poseidon worker")
	w := worker.Worker{
		Queue: *queue.New(),
		Db:    make(map[uuid.UUID]*task.Task),
	}
	wapi := worker.Api{Address: whost, Port: wport, Worker: &w}
	go w.RunTasks()
	go w.CollectStats()
	go wapi.Start()

	fmt.Println("Starting Poseidon manager")
	workers := []string{fmt.Sprintf("%s:%d", whost, wport)}
	m := manager.New(workers)
	mapi := manager.Api{Address: mhost, Port: mport, Manager: m}
	go m.ProcessTasks()
	go m.UpdateTasks()
	mapi.Start()
}
