package drivers

import (
	"fmt"
	"sort"
	"strings"
)

// validAppPlatformRegions is the set of valid `region` slug values for an
// App Platform AppSpec.Region (and equivalently spec.Config["region"] on
// infra.container_service modules).
//
// IMPORTANT: App Platform uses REGIONAL slugs (e.g. "nyc", "sfo") — not the
// Droplet/VPC DATACENTER slugs (e.g. "nyc1", "nyc3", "sfo3"). Sending a
// datacenter slug to the App Platform API yields a misleading
// "404 Image tag or digest not found" rather than a clear validation error,
// so we surface a useful message at plan time.
//
// Source: https://docs.digitalocean.com/products/app-platform/details/limits/
// (Available regions table). Slugs covered as of 2026-05.
var validAppPlatformRegions = map[string]struct{}{
	"ams": {}, // Amsterdam
	"blr": {}, // Bangalore
	"fra": {}, // Frankfurt
	"lon": {}, // London
	"nyc": {}, // New York
	"sfo": {}, // San Francisco
	"sgp": {}, // Singapore
	"syd": {}, // Sydney
	"tor": {}, // Toronto
	"tlv": {}, // Tel Aviv
}

// validateAppPlatformRegion checks `region` against the set of valid App
// Platform regional slugs. Empty input is permitted (caller falls back to
// the driver default). Whitespace is trimmed; comparison is case-insensitive.
//
// On invalid input, the error message names every valid region and explicitly
// calls out the datacenter-slug mistake (the most common one), so the operator
// can fix their config without round-tripping the DO docs.
func validateAppPlatformRegion(region string) error {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil
	}
	if _, ok := validAppPlatformRegions[strings.ToLower(region)]; ok {
		return nil
	}
	return fmt.Errorf(
		"invalid App Platform region %q: must be one of %s "+
			"(datacenter slugs like 'nyc1'/'nyc3'/'sfo3' are NOT valid for App Platform — those are Droplet/VPC slugs)",
		region, sortedAppPlatformRegions())
}

// sortedAppPlatformRegions returns the valid region slugs as a deterministic
// sorted slice for inclusion in error messages.
func sortedAppPlatformRegions() []string {
	out := make([]string, 0, len(validAppPlatformRegions))
	for k := range validAppPlatformRegions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
