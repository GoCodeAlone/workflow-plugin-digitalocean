// Package drivers implements per-resource DigitalOcean drivers used by the
// plugin. shared.go holds helpers that are used by more than one driver.
package drivers

// isUUIDLike returns true when s has the canonical UUID shape:
// 36 characters with hyphens at positions 8, 13, 18, and 23.
//
// Used by drivers whose DO API requires a UUID in the URL path (app platform,
// databases, certificates, load balancers, VPCs, firewalls, droplets, etc.)
// to detect stale state that stored a resource name instead of the UUID,
// so callers can fall back to a name-based lookup instead of sending a
// malformed path parameter to the DO API.
func isUUIDLike(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}
