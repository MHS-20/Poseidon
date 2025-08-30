# Poseidon
Container Orchestrator from scratch
<div align="center">
<img src="logo.png" alt="Logo" width="200"/>
</div>

### Tech Stack
- **Language**: `Go`
- **Containerization**: `Docker Go SDK`
- **Networking**: `chi`
- **Data Storage**: `BoltDB`
- **Metrics**: `goprocinfo`
- **CLI**: `Cobra`
<br/><br>

### Core Components
1) **Task**: the smallest unit of work, specifies a container image to run, resource requirements and a restart policy. 
2) **Scheduler**: responsible for placing tasks on nodes based on resource availability and task requirements, computes a score for each node. 
3) **Manager**: accepts tasks submissions, collects metrics from workers, keeps track of tasks, their states, and the machine on which they run.
4) **Worker**: executes tasks assigned by the manager, reports status and metrics back to the manager.
5) **CLI**: command-line interface for users to interact with the orchestrator, submit tasks, and check status.
<br/><br>

## Features
| Feature                                         | Status |
|-------------------------------------------------|--------|
| Starting, stopping, deleting docker containers  | ✔️     |
| Starting, stopping, deleting tasks              | ✔️     |
| Checking validity of state transitions          | ✔️     |
| Collecting and exposing tasks' metrics          | ✔️     |
| Worker API (task submission, task list, task deletion) | ✔️ |
| Manager control loop                            | ✔️     |
| Scheduling tasks on workers (RoundRobin, E-PVM) | ✔️     |
| Task Health Checks and Restart Policies         | ✔️     |
| Manager API (task submission, task list, task deletion) | ✔️ |
| Persistent storage (BoltDB)                     | ✔️     |
| CLI                                             | ✔️     |
| Service Discovery                               | ❌     |
| High availability                               | ❌     |
| Load balancer                                   | ❌     |
| Security                                        | ❌     |
<br/><br>

## CLI
Start the manager
```
go run main.go manager
```

Start a worker
```
go run main.go worker
  ```

Run a new task:
```
go run main.go task  --filename data/task1.json
```

Check tasks status:
```
go run main.go status
```

Stop a task:
```
go run main.go stop <task_id>
```

Get node information:
```
go run main.go node
```


## API

Manager API: 
```
+---------+-------------------+--------------------------------------------+
| Method  | Route             | Description                                |
+---------+-------------------+--------------------------------------------+
| GET     | /tasks            | Gets a list of all tasks                   |
| POST    | /tasks            | Creates a task                             |
| DELETE  | /tasks/{taskID}   | Stops the task identified by taskID        |
| GET     | /nodes            | Gets a list of nodes                       |
+---------+-------------------+--------------------------------------------+
```

Worker API: 
```
+--------+-------------------+--------------------------------------------+
| Method | Route             | Description                                |
+--------+-------------------+--------------------------------------------+
| GET    | /tasks            | Gets a list of all tasks                   |
| POST   | /tasks            | Creates a task                             |
| DELETE | /tasks/{taskID}   | Stops the task identified by taskID        |
| GET    | /stats            | Gets metrics about the worker              |
+--------+-------------------+--------------------------------------------+

```