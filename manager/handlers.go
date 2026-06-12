package manager

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MHS-20/poseidon/task"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const systemTaskPrefix = "poseidon-"

func isSystemTask(name string) bool {
	return strings.HasPrefix(name, systemTaskPrefix)
}

func (a *Api) GetTasksHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(a.Manager.GetTasks())
}

func (a *Api) StartTaskHandler(w http.ResponseWriter, r *http.Request) {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()

	te := task.TaskEvent{}
	err := d.Decode(&te)
	if err != nil {
		msg := fmt.Sprintf("Error unmarshalling body: %v\n", err)
		log.Print(msg)
		w.WriteHeader(400)
		e := ErrResponse{
			HTTPStatusCode: 400,
			Message:        msg,
		}
		json.NewEncoder(w).Encode(e)
		return
	}

	if isSystemTask(te.Task.Name) {
		msg := fmt.Sprintf("cannot create system task %q via user API", te.Task.Name)
		log.Print(msg)
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(ErrResponse{HTTPStatusCode: 403, Message: msg})
		return
	}

	a.Manager.AddTask(te)
	log.Printf("Added task %v\n", te.Task.ID)
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(te.Task)
}

func (a *Api) StopTaskHandler(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	if taskID == "" {
		log.Printf("No taskID passed in request.\n")
		w.WriteHeader(400)
	}

	tID, _ := uuid.Parse(taskID)
	taskToStop, err := a.Manager.TaskDb.Get(tID.String())
	if err != nil {
		log.Printf("No task with ID %v found", tID)
		w.WriteHeader(404)
	}

	taskCopy := taskToStop.(*task.Task)

	if isSystemTask(taskCopy.Name) {
		msg := fmt.Sprintf("cannot stop system task %q via user API", taskCopy.Name)
		log.Print(msg)
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(ErrResponse{HTTPStatusCode: 403, Message: msg})
		return
	}

	te := task.TaskEvent{
		ID:        uuid.New(),
		State:     task.Completed,
		Timestamp: time.Now(),
	}

	taskCopy.State = task.Completed
	te.Task = *taskCopy
	a.Manager.AddTask(te)

	log.Printf("Added task event %v to stop task %v\n", te.ID, taskCopy.ID)
	w.WriteHeader(204)
}

func (a *Api) GetNodesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(a.Manager.WorkerNodes)
}

func (a *Api) GetStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"isLeader":  a.Manager.IsLeader(),
		"taskCount": len(a.Manager.GetTasks()),
	})
}
