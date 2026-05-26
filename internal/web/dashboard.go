package web

import (
	"net/http"
)

type dashboardData struct {
	Page       string
	Stats      dashboardStats
	Stacks     interface{}
	Containers interface{}
}

type dashboardStats struct {
	Stacks     int
	Containers int
	Running    int
	Images     int
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	stacks, _ := h.store.List()
	containers, _ := h.agent.Engine().ListContainers(r.Context())
	images, _ := h.agent.Engine().ListImages(r.Context())

	running := 0
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
	}

	data := dashboardData{
		Page: "dashboard",
		Stats: dashboardStats{
			Stacks:     len(stacks),
			Containers: len(containers),
			Running:    running,
			Images:     len(images),
		},
		Stacks:     stacks,
		Containers: containers,
	}
	h.render(w, r, "dashboard.html", data)
}
