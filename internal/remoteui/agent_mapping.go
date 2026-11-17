package remoteui

import (
	"errors"

	"github.com/umayr/brine/internal/agentclient"
)

// mapInventory and mapInspection assume their input was already validated by
// agentclient on the verified connection. They do not authenticate the caller,
// check the current registry record, or own the transport lifetime.
func mapInventory(items []agentclient.InventoryItem) []Container {
	out := make([]Container, 0, len(items))
	for _, x := range items {
		out = append(out, Container{ID: x.ID, Name: x.Name, Stack: x.Stack, Service: x.Service, State: x.State, Image: x.Image})
	}
	return out
}

func mapInspection(r agentclient.InspectResult, requestedID string) (Inspection, error) {
	x := r.Inspect
	if !fullID(requestedID) || r.ContainerID != requestedID || x.ID != requestedID {
		return Inspection{}, errors.New("inspection identity mismatch")
	}
	out := Inspection{ID: x.ID, Name: x.Name, Stack: r.Stack, Service: r.Service, ObservedAt: r.ObservedAt, Image: x.Image, ImageID: x.ImageID, State: x.State, CreatedAt: x.CreatedAt, User: x.User, RestartPolicy: x.RestartPolicy, Command: x.Command, EnvKeys: x.EnvKeys, Networks: x.Networks, Labels: x.Labels}
	out.Ports = make([]InspectionPort, 0, len(x.Ports))
	for _, p := range x.Ports {
		out.Ports = append(out.Ports, InspectionPort{Protocol: p.Protocol, HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort})
	}
	out.Mounts = make([]InspectionMount, 0, len(x.Mounts))
	for _, m := range x.Mounts {
		out.Mounts = append(out.Mounts, InspectionMount{Type: m.Type, Source: m.Source, Destination: m.Destination, ReadOnly: m.ReadOnly})
	}
	out.Health = InspectionHealth{Status: x.Health.Status, Test: x.Health.Test}
	out.Resources = InspectionResources{MemoryBytes: x.Resources.MemoryBytes, CPUNano: x.Resources.CPUNano, PidsLimit: x.Resources.PidsLimit}
	out.Security = InspectionSecurity{Privileged: x.Security.Privileged, CapAdd: x.Security.CapAdd, NetworkMode: x.Security.NetworkMode, PIDMode: x.Security.PIDMode, IPCMode: x.Security.IPCMode}
	return out, nil
}
