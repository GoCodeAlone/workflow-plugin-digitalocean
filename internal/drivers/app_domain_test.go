package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

func appWithDomains(domains ...*godo.AppDomainSpec) *godo.App {
	app := testApp()
	app.Spec = &godo.AppSpec{
		Name:   "buymywishlist",
		Region: "nyc",
		Services: []*godo.AppServiceSpec{{
			Name: "web",
			Image: &godo.ImageSourceSpec{
				RegistryType: "DOCR",
				Repository:   "bmw-api",
				Tag:          "sha-abc123",
			},
			Envs: []*godo.AppVariableDefinition{{
				Key:   "DATABASE_URL",
				Value: "${DATABASE_URL}",
				Type:  godo.AppVariableType_Secret,
				Scope: godo.AppVariableScope_RunAndBuildTime,
			}},
		}},
		Domains: domains,
	}
	return app
}

func TestAppDomainDriver_CreateAddsDomainWithoutRebuildingApp(t *testing.T) {
	app := appWithDomains(&godo.AppDomainSpec{
		Domain: "buymywishlist.com",
		Type:   godo.AppDomainSpecType_Primary,
		Zone:   "buymywishlist.com",
	})
	mock := &mockAppClient{app: app, listApps: []*godo.App{app}}
	d := drivers.NewAppDomainDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-www-domain",
		Type: "infra.app_domain",
		Config: map[string]any{
			"app":    "buymywishlist",
			"domain": "www.buymywishlist.com",
			"type":   "ALIAS",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != app.ID+"/www.buymywishlist.com" {
		t.Fatalf("ProviderID = %q, want %q", out.ProviderID, app.ID+"/www.buymywishlist.com")
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("expected App Platform Update to be called")
	}
	got := mock.lastUpdateReq.Spec
	if got == nil {
		t.Fatal("Update spec is nil")
	}
	if len(got.Services) != 1 || got.Services[0].Image == nil || got.Services[0].Image.Tag != "sha-abc123" {
		t.Fatalf("service spec was not preserved: %+v", got.Services)
	}
	if len(got.Services[0].Envs) != 1 || got.Services[0].Envs[0].Key != "DATABASE_URL" {
		t.Fatalf("service env was not preserved: %+v", got.Services[0].Envs)
	}
	if len(got.Domains) != 2 {
		t.Fatalf("domains len = %d, want 2: %+v", len(got.Domains), got.Domains)
	}
	added := got.Domains[1]
	if added.Domain != "www.buymywishlist.com" || added.Type != godo.AppDomainSpecType_Alias {
		t.Fatalf("added domain = %+v, want www alias", added)
	}
}

func TestAppDomainDriver_DiffDetectsMissingAndMatchingDomain(t *testing.T) {
	d := drivers.NewAppDomainDriverWithClient(&mockAppClient{})
	desired := interfaces.ResourceSpec{
		Name: "bmw-www-domain",
		Type: "infra.app_domain",
		Config: map[string]any{
			"domain": "www.buymywishlist.com",
			"type":   "ALIAS",
		},
	}
	missing, err := d.Diff(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Diff missing: %v", err)
	}
	if !missing.NeedsUpdate {
		t.Fatalf("missing domain Diff.NeedsUpdate = false, want true")
	}

	matching, err := d.Diff(context.Background(), desired, &interfaces.ResourceOutput{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: "app-id/www.buymywishlist.com",
		Outputs: map[string]any{
			"domain": "www.buymywishlist.com",
			"type":   "ALIAS",
		},
		Status: "active",
	})
	if err != nil {
		t.Fatalf("Diff matching: %v", err)
	}
	if matching.NeedsUpdate {
		t.Fatalf("matching domain Diff.NeedsUpdate = true, want false: %+v", matching.Changes)
	}
}

func TestAppDomainDriver_DeleteRemovesOnlyDesiredDomain(t *testing.T) {
	app := appWithDomains(
		&godo.AppDomainSpec{Domain: "buymywishlist.com", Type: godo.AppDomainSpecType_Primary},
		&godo.AppDomainSpec{Domain: "www.buymywishlist.com", Type: godo.AppDomainSpecType_Alias},
	)
	mock := &mockAppClient{app: app}
	d := drivers.NewAppDomainDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/www.buymywishlist.com",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if mock.lastUpdateReq == nil || mock.lastUpdateReq.Spec == nil {
		t.Fatal("expected App Platform Update to be called")
	}
	got := mock.lastUpdateReq.Spec.Domains
	if len(got) != 1 || got[0].Domain != "buymywishlist.com" {
		t.Fatalf("remaining domains = %+v, want apex only", got)
	}
}
