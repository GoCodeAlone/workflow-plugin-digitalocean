package internal

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// doModuleInstance wraps a DOProvider as an sdk.ModuleInstance + sdk.ServiceInvoker.
// The host calls InvokeMethod to route IaCProvider and ResourceDriver operations
// across the gRPC plugin boundary.
type doModuleInstance struct {
	provider *DOProvider
}

// ── sdk.ModuleInstance ────────────────────────────────────────────────────────

func (m *doModuleInstance) Init() error                      { return nil }
func (m *doModuleInstance) Start(_ context.Context) error    { return nil }
func (m *doModuleInstance) Stop(_ context.Context) error     { return nil }

// ── sdk.ServiceInvoker ────────────────────────────────────────────────────────

// InvokeMethod dispatches host calls to the underlying DOProvider and its
// resource drivers. Method names follow the convention "Interface.MethodName".
func (m *doModuleInstance) InvokeMethod(method string, args map[string]any) (map[string]any, error) {
	switch method {
	case "IaCProvider.Initialize":
		// Already initialised in CreateModule; accept a re-init call as a no-op.
		return map[string]any{}, nil

	case "IaCProvider.Name":
		return map[string]any{"name": m.provider.Name()}, nil

	case "IaCProvider.Version":
		return map[string]any{"version": m.provider.Version()}, nil

	case "IaCProvider.Capabilities":
		caps := m.provider.Capabilities()
		out := make([]any, len(caps))
		for i, c := range caps {
			out[i] = map[string]any{
				"resource_type": c.ResourceType,
				"tier":          c.Tier,
				"operations":    c.Operations,
			}
		}
		return map[string]any{"capabilities": out}, nil

	case "ResourceDriver.Update":
		return m.invokeDriverUpdate(args)

	case "ResourceDriver.HealthCheck":
		return m.invokeDriverHealthCheck(args)

	case "ResourceDriver.Create", "ResourceDriver.Read", "ResourceDriver.Delete",
		"ResourceDriver.Scale", "ResourceDriver.Diff":
		return nil, fmt.Errorf("digitalocean plugin: %s is not yet supported via remote invocation — use wfctl infra apply", method)

	default:
		return nil, fmt.Errorf("digitalocean plugin: unknown method %q", method)
	}
}

// invokeDriverUpdate decodes args and calls ResourceDriver.Update.
func (m *doModuleInstance) invokeDriverUpdate(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Update: missing resource_type arg")
	}

	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Update: %w", err)
	}

	ref := refFromArgs(args)
	spec, err := specFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Update: %w", err)
	}

	out, err := driver.Update(context.Background(), ref, spec)
	if err != nil {
		return nil, err
	}
	return resourceOutputToMap(out), nil
}

// invokeDriverHealthCheck decodes args and calls ResourceDriver.HealthCheck.
func (m *doModuleInstance) invokeDriverHealthCheck(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.HealthCheck: missing resource_type arg")
	}

	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.HealthCheck: %w", err)
	}

	ref := refFromArgs(args)
	result, err := driver.HealthCheck(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"healthy": result.Healthy,
		"message": result.Message,
	}, nil
}

// ── arg helpers ───────────────────────────────────────────────────────────────

func refFromArgs(args map[string]any) interfaces.ResourceRef {
	return interfaces.ResourceRef{
		Name:       stringArg(args, "ref_name"),
		Type:       stringArg(args, "ref_type"),
		ProviderID: stringArg(args, "ref_provider_id"),
	}
}

func specFromArgs(args map[string]any) (interfaces.ResourceSpec, error) {
	cfg, ok := args["spec_config"]
	if !ok {
		cfg = map[string]any{}
	}
	cfgMap, ok := cfg.(map[string]any)
	if !ok {
		return interfaces.ResourceSpec{}, fmt.Errorf("spec_config must be a map")
	}
	return interfaces.ResourceSpec{
		Name:   stringArg(args, "spec_name"),
		Type:   stringArg(args, "spec_type"),
		Config: cfgMap,
	}, nil
}

func resourceOutputToMap(out *interfaces.ResourceOutput) map[string]any {
	if out == nil {
		return map[string]any{}
	}
	return map[string]any{
		"provider_id": out.ProviderID,
		"name":        out.Name,
		"type":        out.Type,
		"status":      out.Status,
		"outputs":     out.Outputs,
	}
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}
