package drivers

import (
	"strings"
	"testing"
)

// TestValidateAppPlatformRegion_ValidSlugs verifies that every documented App
// Platform regional slug passes validation. Mirrors the source-of-truth list
// from DigitalOcean's App Platform regional limits page.
func TestValidateAppPlatformRegion_ValidSlugs(t *testing.T) {
	valid := []string{"ams", "blr", "fra", "lon", "nyc", "sfo", "sgp", "syd", "tor", "tlv"}
	for _, r := range valid {
		t.Run(r, func(t *testing.T) {
			if err := validateAppPlatformRegion(r); err != nil {
				t.Errorf("validateAppPlatformRegion(%q) returned error: %v", r, err)
			}
		})
	}
}

// TestValidateAppPlatformRegion_ValidSlugs_CaseAndWhitespace confirms that
// the validator normalises input — leading/trailing whitespace and uppercase
// (e.g. yaml that quoted "NYC " or " sfo") still resolve cleanly.
func TestValidateAppPlatformRegion_ValidSlugs_CaseAndWhitespace(t *testing.T) {
	cases := []string{"NYC", "Sfo", "  fra  ", "AMS"}
	for _, r := range cases {
		t.Run(r, func(t *testing.T) {
			if err := validateAppPlatformRegion(r); err != nil {
				t.Errorf("validateAppPlatformRegion(%q) returned error: %v", r, err)
			}
		})
	}
}

// TestValidateAppPlatformRegion_EmptyAccepted confirms that an empty region
// passes (caller falls back to driver default; the driver default is itself
// not validated because it can come from environment defaults the user did
// not author).
func TestValidateAppPlatformRegion_EmptyAccepted(t *testing.T) {
	if err := validateAppPlatformRegion(""); err != nil {
		t.Errorf("validateAppPlatformRegion(\"\") should accept empty: %v", err)
	}
	if err := validateAppPlatformRegion("   "); err != nil {
		t.Errorf("validateAppPlatformRegion(whitespace) should accept whitespace: %v", err)
	}
}

// TestValidateAppPlatformRegion_DatacenterSlugRejected covers the user-facing
// bug: passing a Droplet/VPC datacenter slug (nyc1, nyc3, sfo3, ams3, fra1)
// must fail at plan time with a clear message rather than silently forwarding
// to the DO API and surfacing as "404 Image tag or digest not found".
func TestValidateAppPlatformRegion_DatacenterSlugRejected(t *testing.T) {
	bad := []string{"nyc1", "nyc3", "sfo2", "sfo3", "ams3", "fra1", "lon1", "sgp1", "tor1", "blr1", "syd1"}
	for _, r := range bad {
		t.Run(r, func(t *testing.T) {
			err := validateAppPlatformRegion(r)
			if err == nil {
				t.Fatalf("validateAppPlatformRegion(%q): expected error, got nil", r)
			}
			msg := err.Error()
			// Must name the offending slug.
			if !strings.Contains(msg, r) {
				t.Errorf("error message must include %q: %s", r, msg)
			}
			// Must enumerate valid regions so the operator can self-correct.
			for _, want := range []string{"ams", "blr", "fra", "lon", "nyc", "sfo", "sgp", "syd", "tor", "tlv"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error message must include valid region %q: %s", want, msg)
				}
			}
			// Must call out the datacenter-slug confusion (the actual bug).
			if !strings.Contains(strings.ToLower(msg), "datacenter") {
				t.Errorf("error message must explain that datacenter slugs are invalid for App Platform: %s", msg)
			}
		})
	}
}

// TestValidateAppPlatformRegion_GarbageRejected covers non-DO inputs (e.g.
// AWS region copied by mistake, typos, empty-with-letters).
func TestValidateAppPlatformRegion_GarbageRejected(t *testing.T) {
	bad := []string{"us-east-1", "europe-west2", "newyork", "nycc", "x"}
	for _, r := range bad {
		t.Run(r, func(t *testing.T) {
			if err := validateAppPlatformRegion(r); err == nil {
				t.Fatalf("validateAppPlatformRegion(%q): expected error, got nil", r)
			}
		})
	}
}

