package drivers

// Test-only exports. The _test.go suffix ensures these are only compiled
// during `go test`, never into the production binary.
var (
	DeploymentProgressStringForTest    = deploymentProgressString
	DeploymentHealthErrorForTest       = deploymentHealthError
	SanitizeClonedSpecForCreateForTest = sanitizeClonedSpecForCreate
)
