package model

// DeploymentFailureKind is the terminal failure of a node installation job.
// Verification, cleanup and ongoing health failures still require acknowledgement.
const DeploymentFailureKind = "job.install"

func (c NotificationCondition) RequiresAcknowledgement() bool {
	return c.Kind != DeploymentFailureKind
}
