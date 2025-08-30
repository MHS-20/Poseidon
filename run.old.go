package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/MHS-20/poseidon/manager"
	"github.com/MHS-20/poseidon/worker"
)

func main() {
	whost := os.Getenv("POSEIDON_WORKER_HOST")
	wport, _ := strconv.Atoi(os.Getenv("POSEIDON_WORKER_PORT"))
	mhost := os.Getenv("POSEIDON_MANAGER_HOST")
	mport, _ := strconv.Atoi(os.Getenv("POSEIDON_MANAGER_PORT"))

	fmt.Println("Starting Poseidon worker")
	w1 := worker.New("worker-1", "persistent")
	w2 := worker.New("worker-2", "persistent")
	w3 := worker.New("worker-3", "persistent")
	wapi1 := worker.Api{Address: whost, Port: wport, Worker: w1}
	wapi2 := worker.Api{Address: whost, Port: wport + 1, Worker: w2}
	wapi3 := worker.Api{Address: whost, Port: wport + 2, Worker: w3}

	go w1.RunTasks()
	go w1.UpdateTasks()
	go wapi1.Start()
	go w2.RunTasks()
	go w2.UpdateTasks()
	go wapi2.Start()
	go w3.RunTasks()
	go w3.UpdateTasks()
	go wapi3.Start()

	workers := []string{
		fmt.Sprintf("%s:%d", whost, wport),
		fmt.Sprintf("%s:%d", whost, wport+1),
		fmt.Sprintf("%s:%d", whost, wport+2),
	}

	fmt.Println("Starting Poseidon manager")
	// workers := []string{fmt.Sprintf("%s:%d", whost, wport)}
	m := manager.New(workers, "epvm", "persistent")
	mapi := manager.Api{Address: mhost, Port: mport, Manager: m}

	go m.ProcessTasks()
	go m.UpdateTasks()
	go m.DoHealthChecks()
	mapi.Start()
}
