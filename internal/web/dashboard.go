package web

import (
	"net/http"
)

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	stacks, stackErr := h.store.List()
	containers, containerErr := h.agent.Engine().ListContainers(r.Context())
	images, imageErr := h.agent.Engine().ListImages(r.Context())

	running := 0
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
	}

	data := dashboardData{
		pageData: pageData{Page: "dashboard"},
		Stats: dashboardStats{
			Stacks:     len(stacks),
			Containers: len(containers),
			Running:    running,
			Images:     len(images),
		},
		Stacks:                stacks,
		Containers:            containers,
		StacksUnavailable:     stackErr != nil,
		ContainersUnavailable: containerErr != nil,
		ImagesUnavailable:     imageErr != nil,
	}
	h.render(w, r, "dashboard.html", &data)
}
