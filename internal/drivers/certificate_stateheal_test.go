package drivers

// State-heal tests for CertificateDriver.Update / Delete.
// Certificate.Update is delete-then-recreate; heal is exercised through Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type certificateStateHealMock struct {
	listCerts []godo.Certificate
	listErr   error
	listCalls int

	deleteCalledID string
	deleteErr      error

	createCert *godo.Certificate
	createErr  error

	// getCert is returned by Get (used by HealthCheck after heal).
	getCert *godo.Certificate
}

func (m *certificateStateHealMock) Create(_ context.Context, _ *godo.CertificateRequest) (*godo.Certificate, *godo.Response, error) {
	return m.createCert, nil, m.createErr
}
func (m *certificateStateHealMock) Get(_ context.Context, _ string) (*godo.Certificate, *godo.Response, error) {
	if m.getCert != nil {
		return m.getCert, nil, nil
	}
	return nil, nil, errors.New("not implemented in certificateStateHealMock")
}
func (m *certificateStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]godo.Certificate, *godo.Response, error) {
	m.listCalls++
	return m.listCerts, nil, m.listErr
}
func (m *certificateStateHealMock) Delete(_ context.Context, certID string) (*godo.Response, error) {
	m.deleteCalledID = certID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCertificateDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &certificateStateHealMock{
		createCert: &godo.Certificate{ID: wantUUID, Name: "my-cert"},
	}
	d := NewCertificateDriverWithClient(m)
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cert",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-cert" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update (delete-then-recreate) ─────────────────────────────────────────────

func TestCertificateDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &certificateStateHealMock{
		// No list needed — ProviderID is already a valid UUID.
		createCert: &godo.Certificate{ID: "a1b2c3d4-0000-0000-0000-ef1234567891", Name: "my-cert"},
	}
	d := NewCertificateDriverWithClient(m)
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cert", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-cert", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want %q", m.deleteCalledID, uuid)
	}
	if m.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", m.listCalls)
	}
}

func TestCertificateDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &certificateStateHealMock{
		listCerts: []godo.Certificate{{ID: uuid, Name: "my-cert"}},
		createCert: &godo.Certificate{ID: "a1b2c3d4-0000-0000-0000-ef1234567891", Name: "my-cert"},
	}
	d := NewCertificateDriverWithClient(m)
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cert", ProviderID: "my-cert"}, // stale name
		interfaces.ResourceSpec{Name: "my-cert", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update with stale name: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (heal must fire)", m.listCalls)
	}
	// Delete (inside Update) must have been called with the healed UUID.
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

func TestCertificateDriver_Update_HealFails_WhenResourceNotFound(t *testing.T) {
	m := &certificateStateHealMock{listErr: errors.New("api unavailable")}
	d := NewCertificateDriverWithClient(m)
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cert", ProviderID: "my-cert"},
		interfaces.ResourceSpec{Name: "my-cert", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestCertificateDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &certificateStateHealMock{
		listCerts: []godo.Certificate{{ID: uuid, Name: "my-cert"}},
	}
	d := NewCertificateDriverWithClient(m)
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-cert", ProviderID: "my-cert"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

// ── HealthCheck state-heal tests ─────────────────────────────────────────────

func TestCertificateDriver_HealthCheck_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &certificateStateHealMock{
		listCerts: []godo.Certificate{{ID: uuid, Name: "my-cert"}},
		getCert:   &godo.Certificate{ID: uuid, Name: "my-cert", State: "verified"},
	}
	d := NewCertificateDriverWithClient(m)
	ref := interfaces.ResourceRef{Name: "my-cert", ProviderID: "my-cert"} // stale name
	result, err := d.HealthCheck(context.Background(), ref)
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (resolve must fire for stale name)", m.listCalls)
	}
	if !result.Healthy {
		t.Errorf("Healthy = false, want true after state-heal")
	}
}
