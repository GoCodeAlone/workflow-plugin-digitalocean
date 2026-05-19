package drivers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestAppDomainDriver_CreateRejectsDefaultDomainType(t *testing.T) {
	app := appWithDomains()
	mock := &mockAppClient{app: app, listApps: []*godo.App{app}}
	d := drivers.NewAppDomainDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-www-domain",
		Type: "infra.app_domain",
		Config: map[string]any{
			"app":    "buymywishlist",
			"domain": "www.buymywishlist.com",
			"type":   "DEFAULT",
		},
	})
	if err == nil {
		t.Fatal("expected DEFAULT custom domain type to be rejected")
	}
	if !strings.Contains(err.Error(), "DEFAULT") || !strings.Contains(err.Error(), "PRIMARY or ALIAS") {
		t.Fatalf("error = %q, want targeted DEFAULT guidance", err)
	}
	if mock.lastUpdateReq != nil {
		t.Fatal("DEFAULT validation should fail before App Platform Update")
	}
}

func TestAppDomainDriver_ReadIncludesLiveDomainStatus(t *testing.T) {
	app := appWithDomains(&godo.AppDomainSpec{
		Domain:   "www.buymywishlist.com",
		Type:     godo.AppDomainSpecType_Alias,
		Zone:     "buymywishlist.com",
		Wildcard: true,
	})
	app.Domains = []*godo.AppDomain{{
		ID:    "domain-1",
		Spec:  &godo.AppDomainSpec{Domain: "www.buymywishlist.com"},
		Phase: godo.AppJobSpecKindPHASE_Configuring,
		Validation: &godo.AppDomainValidation{
			TXTName:  "_acme-challenge.www",
			TXTValue: "txt-value",
		},
		Validations: []*godo.AppDomainValidation{{
			TXTName:  "_acme-challenge.www",
			TXTValue: "txt-value",
		}},
		Progress: &godo.AppDomainProgress{Steps: []*godo.AppDomainProgressStep{{
			Name:   "certificate",
			Status: godo.AppJobSpecKindProgressStepStatus_Running,
		}}},
	}}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppDomainDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/www.buymywishlist.com",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Status != "configuring" {
		t.Fatalf("Status = %q, want configuring", out.Status)
	}
	if got := out.Outputs["phase"]; got != "CONFIGURING" {
		t.Fatalf("phase output = %#v, want CONFIGURING", got)
	}
	if got := out.Outputs["validation_txt_name"]; got != "_acme-challenge.www" {
		t.Fatalf("validation_txt_name = %#v, want TXT name", got)
	}
	if got := out.Outputs["validation_txt_value"]; got != "txt-value" {
		t.Fatalf("validation_txt_value = %#v, want TXT value", got)
	}
	if got := out.Outputs["progress"]; got != "certificate: RUNNING" {
		t.Fatalf("progress = %#v, want certificate progress", got)
	}
	if _, err := structpb.NewStruct(out.Outputs); err != nil {
		t.Fatalf("outputs are not structpb-compatible: %v", err)
	}
	validations, ok := out.Outputs["validations"].([]any)
	if !ok || len(validations) != 1 {
		t.Fatalf("validations = %#v, want []any with one element", out.Outputs["validations"])
	}
	validation, ok := validations[0].(map[string]any)
	if !ok {
		t.Fatalf("validations[0] = %#v, want map[string]any", validations[0])
	}
	if validation["txt_name"] != "_acme-challenge.www" || validation["txt_value"] != "txt-value" {
		t.Fatalf("validation = %#v, want TXT details", validation)
	}
}

func TestAppDomainDriver_HealthCheckReportsLiveDomainError(t *testing.T) {
	app := appWithDomains(&godo.AppDomainSpec{Domain: "www.buymywishlist.com", Type: godo.AppDomainSpecType_Alias})
	app.Domains = []*godo.AppDomain{{
		Spec:  &godo.AppDomainSpec{Domain: "www.buymywishlist.com"},
		Phase: godo.AppJobSpecKindPHASE_Error,
		Progress: &godo.AppDomainProgress{Steps: []*godo.AppDomainProgressStep{{
			Name:   "certificate",
			Status: godo.AppJobSpecKindProgressStepStatus_Error,
			Reason: &godo.AppDomainProgressStepReason{
				Code:    "validation_failed",
				Message: "CNAME target is invalid",
			},
		}}},
	}}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppDomainDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/www.buymywishlist.com",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Fatal("HealthCheck healthy = true, want false")
	}
	if !strings.Contains(result.Message, "ERROR") || !strings.Contains(result.Message, "validation_failed") || !strings.Contains(result.Message, "CNAME target is invalid") {
		t.Fatalf("HealthCheck message = %q, want phase and progress reason", result.Message)
	}
}

func TestAppDomainDriver_HealthCheckRequiresLiveDomainActive(t *testing.T) {
	app := appWithDomains(&godo.AppDomainSpec{Domain: "www.buymywishlist.com", Type: godo.AppDomainSpecType_Alias})
	app.Domains = []*godo.AppDomain{{
		Spec:  &godo.AppDomainSpec{Domain: "www.buymywishlist.com"},
		Phase: godo.AppJobSpecKindPHASE_Pending,
	}}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppDomainDriverWithClient(mock)

	pending, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/www.buymywishlist.com",
	})
	if err != nil {
		t.Fatalf("HealthCheck pending: %v", err)
	}
	if pending.Healthy || !strings.Contains(pending.Message, "PENDING") {
		t.Fatalf("pending result = %+v, want unhealthy PENDING", pending)
	}

	app.Domains[0].Phase = godo.AppJobSpecKindPHASE_Active
	active, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/www.buymywishlist.com",
	})
	if err != nil {
		t.Fatalf("HealthCheck active: %v", err)
	}
	if !active.Healthy || !strings.Contains(active.Message, "ACTIVE") {
		t.Fatalf("active result = %+v, want healthy ACTIVE", active)
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

func TestAppDomainDriver_DiffTreatsAppTargetChangeAsReplacement(t *testing.T) {
	d := drivers.NewAppDomainDriverWithClient(&mockAppClient{})
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-www-domain",
		Type: "infra.app_domain",
		Config: map[string]any{
			"app":    "other-app",
			"domain": "www.buymywishlist.com",
			"type":   "ALIAS",
		},
	}, &interfaces.ResourceOutput{
		Name:       "bmw-www-domain",
		Type:       "infra.app_domain",
		ProviderID: "app-id/www.buymywishlist.com",
		Outputs: map[string]any{
			"app":    "buymywishlist",
			"app_id": "app-id",
			"domain": "www.buymywishlist.com",
			"type":   "ALIAS",
		},
		Status: "active",
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsReplace {
		t.Fatalf("app target change NeedsReplace = false, want true: %+v", result.Changes)
	}
}

func TestAppDomainDriver_CreateRejectsUndocumentedAppNameAlias(t *testing.T) {
	app := appWithDomains()
	mock := &mockAppClient{app: app, listApps: []*godo.App{app}}
	d := drivers.NewAppDomainDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-www-domain",
		Type: "infra.app_domain",
		Config: map[string]any{
			"app_name": "buymywishlist",
			"domain":   "www.buymywishlist.com",
			"type":     "ALIAS",
		},
	})
	if err == nil {
		t.Fatal("expected undocumented app_name alias to be rejected")
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
