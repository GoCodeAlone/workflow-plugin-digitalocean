package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// CertificateClient is the godo Certificates interface (for mocking).
type CertificateClient interface {
	Create(ctx context.Context, req *godo.CertificateRequest) (*godo.Certificate, *godo.Response, error)
	Get(ctx context.Context, certID string) (*godo.Certificate, *godo.Response, error)
	Delete(ctx context.Context, certID string) (*godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.Certificate, *godo.Response, error)
}

// CertificateDriver manages DigitalOcean SSL/TLS certificates (infra.certificate).
type CertificateDriver struct {
	client CertificateClient
}

// NewCertificateDriver creates a CertificateDriver backed by a real godo client.
func NewCertificateDriver(c *godo.Client) *CertificateDriver {
	return &CertificateDriver{client: c.Certificates}
}

// NewCertificateDriverWithClient creates a driver with an injected client (for tests).
func NewCertificateDriverWithClient(c CertificateClient) *CertificateDriver {
	return &CertificateDriver{client: c}
}

func (d *CertificateDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	certType := strFromConfig(spec.Config, "type", "lets_encrypt")

	req := &godo.CertificateRequest{
		Name: spec.Name,
		Type: certType,
	}

	if domains, ok := spec.Config["dns_names"].([]any); ok {
		for _, dn := range domains {
			if s, ok := dn.(string); ok {
				req.DNSNames = append(req.DNSNames, s)
			}
		}
	}

	cert, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("certificate create %q: %w", spec.Name, WrapGodoError(err))
	}
	if cert == nil || cert.ID == "" {
		return nil, fmt.Errorf("certificate create %q: API returned certificate with empty ID", spec.Name)
	}
	return certOutput(cert, spec.Name), nil
}

func (d *CertificateDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	cert, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("certificate read %q: %w", ref.Name, WrapGodoError(err))
	}
	return certOutput(cert, ref.Name), nil
}

// findCertificateByName iterates the paginated certificate list and returns
// the first certificate whose Name matches. Returns ErrResourceNotFound if
// no match is found.
func (d *CertificateDriver) findCertificateByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		certs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("certificate list: %w", WrapGodoError(err))
		}
		for i := range certs {
			if certs[i].Name == name {
				return certOutput(&certs[i], name), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("certificate %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *CertificateDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: certificate %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findCertificateByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("certificate state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *CertificateDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	// Certificates are immutable; delete and recreate.
	if err := d.Delete(ctx, ref); err != nil {
		return nil, err
	}
	return d.Create(ctx, spec)
}

func (d *CertificateDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("certificate delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *CertificateDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *CertificateDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	cert, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := cert.State == "verified"
	return &interfaces.HealthResult{Healthy: healthy, Message: cert.State}, nil
}

func (d *CertificateDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("certificate does not support scale operation")
}

func certOutput(cert *godo.Certificate, name string) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.certificate",
		ProviderID: cert.ID,
		Outputs: map[string]any{
			"state":       cert.State,
			"not_after":   cert.NotAfter,
			"sha1_finger": cert.SHA1Fingerprint,
		},
		Status: cert.State,
	}
}

func (d *CertificateDriver) SensitiveKeys() []string { return nil }

func (d *CertificateDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }
