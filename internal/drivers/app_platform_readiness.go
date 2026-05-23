package drivers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

const appPlatformDomainProbeTimeout = 5 * time.Second

// AppPlatformDomainProbe checks whether a custom domain is user-visible over
// HTTPS for the app's readiness path.
type AppPlatformDomainProbe func(ctx context.Context, domain, path string) error

var appPlatformDomainHTTPClient = &http.Client{Timeout: appPlatformDomainProbeTimeout}

func appPlatformCustomDomainReadinessError(ctx context.Context, appName string, app *godo.App, probe AppPlatformDomainProbe, pathOverride string) error {
	domains := appPlatformCustomDomains(app)
	if len(domains) == 0 {
		return nil
	}
	for _, spec := range domains {
		live := findLiveAppDomain(app, spec.Domain)
		if live == nil {
			return fmt.Errorf("app %q custom domain %q waiting for live domain status", appName, spec.Domain)
		}
		if live.Phase != godo.AppJobSpecKindPHASE_Active {
			return fmt.Errorf("app %q custom domain %q %s", appName, spec.Domain, liveDomainHealthMessage(live))
		}
	}
	if probe == nil {
		probe = defaultAppPlatformDomainProbe
	}
	path := appPlatformReadinessPath(app, pathOverride)
	for _, spec := range domains {
		if spec.Wildcard {
			continue
		}
		if err := probe(ctx, spec.Domain, path); err != nil {
			return fmt.Errorf("app %q custom domain %q HTTPS readiness %s: %w", appName, spec.Domain, path, err)
		}
	}
	return nil
}

// appPlatformProbeCustomDomains GETs each non-wildcard custom domain on app
// and returns how many were reachable (probe returned nil) and how many were
// attempted in total. Wildcard entries are excluded (no concrete host). Empty
// or nil custom-domain list returns (0, 0). Intended for in-rollout health
// observation; the post-Active gate continues to use
// appPlatformCustomDomainReadinessError.
func appPlatformProbeCustomDomains(ctx context.Context, app *godo.App, probe AppPlatformDomainProbe, pathOverride string) (reachable, total int) {
	domains := appPlatformCustomDomains(app)
	if len(domains) == 0 {
		return 0, 0
	}
	if probe == nil {
		probe = defaultAppPlatformDomainProbe
	}
	path := appPlatformReadinessPath(app, pathOverride)
	for _, spec := range domains {
		if spec.Wildcard {
			continue
		}
		total++
		if err := probe(ctx, spec.Domain, path); err == nil {
			reachable++
		}
	}
	return reachable, total
}

func appPlatformCustomDomains(app *godo.App) []*godo.AppDomainSpec {
	if app == nil || app.Spec == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]*godo.AppDomainSpec, 0, len(app.Spec.Domains))
	for _, spec := range app.Spec.Domains {
		if spec == nil || strings.TrimSpace(spec.Domain) == "" || spec.Type == godo.AppDomainSpecType_Default {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(spec.Domain))
		if seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, spec)
	}
	return out
}

func appPlatformReadinessPath(app *godo.App, override string) string {
	if path := normalizeReadinessPath(override); path != "" {
		return path
	}
	if app != nil && app.Spec != nil {
		for _, svc := range app.Spec.Services {
			if svc == nil || svc.HealthCheck == nil {
				continue
			}
			if path := normalizeReadinessPath(svc.HealthCheck.HTTPPath); path != "" {
				return path
			}
		}
	}
	return "/"
}

func normalizeReadinessPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		parsed, err := url.Parse(path)
		if err == nil {
			path = parsed.RequestURI()
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func defaultAppPlatformDomainProbe(ctx context.Context, domain, path string) error {
	u := appPlatformDomainReadinessURL(domain, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "workflow-plugin-digitalocean/readiness")
	resp, err := appPlatformDomainHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("GET %s returned %s", u, resp.Status)
	}
	return nil
}

func appPlatformDomainReadinessURL(domain, path string) string {
	u := &url.URL{Scheme: "https", Host: strings.TrimSpace(domain)}
	if parsed, err := url.ParseRequestURI(appPlatformReadinessPath(nil, path)); err == nil {
		u.Path = parsed.Path
		u.RawQuery = parsed.RawQuery
	} else {
		u.Path = "/"
	}
	return u.String()
}
