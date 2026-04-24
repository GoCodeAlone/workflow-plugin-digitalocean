package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockCertClient struct {
	cert *godo.Certificate
	err  error
}

func (m *mockCertClient) Create(_ context.Context, _ *godo.CertificateRequest) (*godo.Certificate, *godo.Response, error) {
	return m.cert, nil, m.err
}
func (m *mockCertClient) Get(_ context.Context, _ string) (*godo.Certificate, *godo.Response, error) {
	return m.cert, nil, m.err
}
func (m *mockCertClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockCertClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.Certificate, *godo.Response, error) {
	if m.cert != nil {
		return []godo.Certificate{*m.cert}, nil, m.err
	}
	return nil, nil, m.err
}

func testCertificate() *godo.Certificate {
	return &godo.Certificate{
		ID:              "cert-123",
		Name:            "my-cert",
		State:           "verified",
		SHA1Fingerprint: "abc123",
		NotAfter:        "2027-01-01T00:00:00Z",
	}
}

func TestCertificateDriver_Create(t *testing.T) {
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-cert",
		Config: map[string]any{
			"type":     "lets_encrypt",
			"dns_names": []any{"example.com", "*.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "cert-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cert-123")
	}
	if out.Status != "verified" {
		t.Errorf("Status = %q, want %q", out.Status, "verified")
	}
}

func TestCertificateDriver_Read(t *testing.T) {
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "cert-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cert-123")
	}
}

func TestCertificateDriver_HealthCheck_Verified(t *testing.T) {
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy certificate, got message: %s", result.Message)
	}
}

func TestCertificateDriver_Create_Error(t *testing.T) {
	mock := &mockCertClient{err: fmt.Errorf("api failure")}
	d := drivers.NewCertificateDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cert",
		Config: map[string]any{"type": "lets_encrypt"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCertificateDriver_Update_Success(t *testing.T) {
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	}, interfaces.ResourceSpec{
		Name:   "my-cert",
		Config: map[string]any{"type": "lets_encrypt", "dns_names": []any{"example.com"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "cert-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cert-123")
	}
}

func TestCertificateDriver_Delete(t *testing.T) {
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestCertificateDriver_Delete_Error(t *testing.T) {
	mock := &mockCertClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewCertificateDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCertificateDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockCertClient{}
	d := drivers.NewCertificateDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-cert"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestCertificateDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockCertClient{}
	d := drivers.NewCertificateDriverWithClient(mock)

	current := &interfaces.ResourceOutput{ProviderID: "cert-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-cert"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestCertificateDriver_HealthCheck_Unhealthy(t *testing.T) {
	cert := &godo.Certificate{
		ID:    "cert-123",
		Name:  "my-cert",
		State: "pending",
	}
	mock := &mockCertClient{cert: cert}
	d := drivers.NewCertificateDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-cert", ProviderID: "cert-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for pending certificate")
	}
}

func TestCertificateDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but certificate has empty ID — guard must reject it.
	mock := &mockCertClient{cert: &godo.Certificate{Name: "my-cert"}}
	d := drivers.NewCertificateDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cert",
		Config: map[string]any{"type": "lets_encrypt"},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestCertificateDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockCertClient{cert: testCertificate()}
	d := drivers.NewCertificateDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cert",
		Config: map[string]any{"type": "lets_encrypt"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-cert" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-cert", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
