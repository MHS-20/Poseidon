package manager

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Api struct {
	Address string
	Port    int
	Manager *Manager
	Router  *chi.Mux
}

type ErrResponse struct {
	HTTPStatusCode int
	Message        string
}

func (a *Api) initRouter() {
	a.Router = chi.NewRouter()
	a.Router.Route("/tasks", func(r chi.Router) {
		r.Post("/", a.StartTaskHandler)
		r.Get("/", a.GetTasksHandler)
		r.Route("/{taskID}", func(r chi.Router) {
			r.Delete("/", a.StopTaskHandler)
		})
	})
}

func (a *Api) Start() {
	a.initRouter()
	addr := fmt.Sprintf("%s:%d", a.Address, a.Port)
	fmt.Printf("API server starting on %s\n", addr)
	err := http.ListenAndServe(addr, a.Router)
	if err != nil {
		fmt.Printf("Error starting server: %v\n", err)
	}
}
