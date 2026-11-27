package remoteui

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/projectsnapshot"
)

func (h *Deployment) observePlan(ctx context.Context, meta ExecutionMetadata, c deploymentStager, epoch, stack string, snap projectsnapshot.Snapshot) (stage, manifest string, plan *agentclient.DeploymentPreview, e error) {
	meta.Action = "plan"
	_, e = h.observeTarget(ctx, meta, func() (agentclient.DeploymentStatus, error) {
		var err error
		stage, manifest, plan, err = stageAndPreview(ctx, c, epoch, stack, snap)
		return agentclient.DeploymentStatus{}, err
	})
	return
}
