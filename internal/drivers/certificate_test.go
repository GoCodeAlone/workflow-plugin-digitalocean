package drivers_test

import (
	"context"
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
