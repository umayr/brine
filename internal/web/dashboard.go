package web

import (
	"net/http"
	"net/url"
	"time"
)

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{pageData: pageData{Page: "dashboard"}}
	observe := func(name string, err error, route string) {
		if err != nil {
			data.Attention = append(data.Attention, dashboardAlert{"Inventory unavailable", name, "No current inventory was returned.", route, "Open " + name})
			return
		}
		data.Observations = append(data.Observations, dashboardObservation{name, time.Now().UTC()})
	}

	stacks, stackErr := h.store.List()
	observe("Stacks", stackErr, "/stacks")
	containers, containerErr := h.agent.Engine().ListContainers(r.Context())
	observe("Containers", containerErr, "/containers")
	images, imageErr := h.agent.Engine().ListImages(r.Context())
	observe("Images", imageErr, "/images")

	data.StacksUnavailable = stackErr != nil
	data.ContainersUnavailable = containerErr != nil
	data.ImagesUnavailable = imageErr != nil
	data.Stats = dashboardStats{Stacks: len(stacks), Containers: len(containers), Images: len(images)}
	if containerErr == nil {
		for _, c := range containers {
			switch c.State {
			case "running":
				data.Stats.Running++
			case "exited", "dead", "restarting":
				// Report observed state, not service health or an unexpected exit.
				// An unmanaged exited container may be an intentionally finished job.
				if c.Stack != "" || c.State != "exited" {
					data.Attention = append(data.Attention, dashboardAlert{"Container " + c.State, c.Name, c.Status, "/containers/" + url.PathEscape(c.ID), "Inspect container"})
				}
			}
		}
	}
	h.render(w, r, "dashboard.html", &data)
}
