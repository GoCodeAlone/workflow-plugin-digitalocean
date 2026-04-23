package internal

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// doModuleInstance wraps an IaCProvider as an sdk.ModuleInstance + sdk.ServiceInvoker.
// The host calls InvokeMethod to route IaCProvider and ResourceDriver operations
// across the gRPC plugin boundary.
type doModuleInstance struct {
	provider interfaces.IaCProvider
}

// ── sdk.ModuleInstance ────────────────────────────────────────────────────────

func (m *doModuleInstance) Init() error                   { return nil }
func (m *doModuleInstance) Start(_ context.Context) error { return nil }
func (m *doModuleInstance) Stop(_ context.Context) error  { return nil }

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

	case "IaCProvider.Plan":
		return m.invokeProviderPlan(args)

	case "IaCProvider.Apply":
		return m.invokeProviderApply(args)

	case "IaCProvider.Destroy":
		return m.invokeProviderDestroy(args)

	case "IaCProvider.Status":
		return m.invokeProviderStatus(args)

	case "IaCProvider.DetectDrift":
		return m.invokeProviderDetectDrift(args)

	case "IaCProvider.Import":
		return m.invokeProviderImport(args)

	case "IaCProvider.ResolveSizing":
		return m.invokeProviderResolveSizing(args)

	case "IaCProvider.BootstrapStateBackend":
		return m.invokeProviderBootstrapStateBackend(args)

	case "ResourceDriver.Update":
		return m.invokeDriverUpdate(args)

	case "ResourceDriver.HealthCheck":
		return m.invokeDriverHealthCheck(args)

	case "ResourceDriver.Create":
		return m.invokeDriverCreate(args)

	case "ResourceDriver.Read":
		return m.invokeDriverRead(args)

	case "ResourceDriver.Delete":
		return m.invokeDriverDelete(args)

	case "ResourceDriver.Diff":
		return m.invokeDriverDiff(args)

	case "ResourceDriver.Scale":
		return m.invokeDriverScale(args)

	case "ResourceDriver.SensitiveKeys":
		return m.invokeDriverSensitiveKeys(args)

	default:
		return nil, fmt.Errorf("digitalocean plugin: unknown method %q", method)
	}
}

// ── IaCProvider bulk-method helpers ──────────────────────────────────────────

// invokeProviderPlan decodes desired+current and calls IaCProvider.Plan.
func (m *doModuleInstance) invokeProviderPlan(args map[string]any) (map[string]any, error) {
	var desired []interfaces.ResourceSpec
	if err := decodeJSONField(args, "desired", &desired); err != nil {
		return nil, fmt.Errorf("IaCProvider.Plan: %w", err)
	}
	var current []interfaces.ResourceState
	if err := decodeJSONField(args, "current", &current); err != nil {
		return nil, fmt.Errorf("IaCProvider.Plan: %w", err)
	}
	plan, err := m.provider.Plan(context.Background(), desired, current)
	if err != nil {
		return nil, err
	}
	return structToMap(plan)
}

// invokeProviderApply decodes the plan and calls IaCProvider.Apply.
func (m *doModuleInstance) invokeProviderApply(args map[string]any) (map[string]any, error) {
	var plan interfaces.IaCPlan
	if err := decodeJSONField(args, "plan", &plan); err != nil {
		return nil, fmt.Errorf("IaCProvider.Apply: %w", err)
	}
	result, err := m.provider.Apply(context.Background(), &plan)
	if err != nil {
		return nil, err
	}
	return structToMap(result)
}

// invokeProviderDestroy decodes refs and calls IaCProvider.Destroy.
func (m *doModuleInstance) invokeProviderDestroy(args map[string]any) (map[string]any, error) {
	refs, err := refsFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("IaCProvider.Destroy: %w", err)
	}
	result, err := m.provider.Destroy(context.Background(), refs)
	if err != nil {
		return nil, err
	}
	return structToMap(result)
}

// invokeProviderStatus decodes refs and calls IaCProvider.Status.
func (m *doModuleInstance) invokeProviderStatus(args map[string]any) (map[string]any, error) {
	refs, err := refsFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("IaCProvider.Status: %w", err)
	}
	statuses, err := m.provider.Status(context.Background(), refs)
	if err != nil {
		return nil, err
	}
	statusList := make([]any, len(statuses))
	for i, s := range statuses {
		sm, _ := structToMap(s)
		statusList[i] = sm
	}
	return map[string]any{"statuses": statusList}, nil
}

// invokeProviderDetectDrift decodes refs and calls IaCProvider.DetectDrift.
func (m *doModuleInstance) invokeProviderDetectDrift(args map[string]any) (map[string]any, error) {
	refs, err := refsFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("IaCProvider.DetectDrift: %w", err)
	}
	drifts, err := m.provider.DetectDrift(context.Background(), refs)
	if err != nil {
		return nil, err
	}
	driftList := make([]any, len(drifts))
	for i, d := range drifts {
		dm, _ := structToMap(d)
		driftList[i] = dm
	}
	return map[string]any{"drifts": driftList}, nil
}

// invokeProviderImport decodes resource_type + provider_id and calls IaCProvider.Import.
func (m *doModuleInstance) invokeProviderImport(args map[string]any) (map[string]any, error) {
	resourceType := stringArg(args, "resource_type")
	providerID := stringArg(args, "provider_id")
	state, err := m.provider.Import(context.Background(), providerID, resourceType)
	if err != nil {
		return nil, err
	}
	return structToMap(state)
}

// invokeProviderResolveSizing decodes resource_type + size + hints and calls IaCProvider.ResolveSizing.
func (m *doModuleInstance) invokeProviderResolveSizing(args map[string]any) (map[string]any, error) {
	resourceType := stringArg(args, "resource_type")
	size := interfaces.Size(stringArg(args, "size"))
	var hints *interfaces.ResourceHints
	if h, ok := args["hints"]; ok && h != nil {
		hints = &interfaces.ResourceHints{}
		if err := decodeJSONValue(h, hints); err != nil {
			return nil, fmt.Errorf("IaCProvider.ResolveSizing: %w", err)
		}
	}
	sizing, err := m.provider.ResolveSizing(resourceType, size, hints)
	if err != nil {
		return nil, err
	}
	return structToMap(sizing)
}

// invokeProviderBootstrapStateBackend decodes the cfg map and calls
// IaCProvider.BootstrapStateBackend, returning the result as a flat map.
func (m *doModuleInstance) invokeProviderBootstrapStateBackend(args map[string]any) (map[string]any, error) {
	var cfg map[string]any
	if args != nil {
		cfg, _ = args["cfg"].(map[string]any)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	result, err := m.provider.BootstrapStateBackend(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{}, nil
	}
	return structToMap(result)
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

// invokeDriverCreate decodes args and calls ResourceDriver.Create.
func (m *doModuleInstance) invokeDriverCreate(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Create: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Create: %w", err)
	}
	spec, err := specFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Create: %w", err)
	}
	out, err := driver.Create(context.Background(), spec)
	if err != nil {
		return nil, err
	}
	return resourceOutputToMap(out), nil
}

// invokeDriverRead decodes args and calls ResourceDriver.Read.
func (m *doModuleInstance) invokeDriverRead(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Read: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Read: %w", err)
	}
	out, err := driver.Read(context.Background(), refFromArgs(args))
	if err != nil {
		return nil, err
	}
	return resourceOutputToMap(out), nil
}

// invokeDriverDelete decodes args and calls ResourceDriver.Delete.
func (m *doModuleInstance) invokeDriverDelete(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Delete: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Delete: %w", err)
	}
	if err := driver.Delete(context.Background(), refFromArgs(args)); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// invokeDriverDiff decodes args and calls ResourceDriver.Diff.
func (m *doModuleInstance) invokeDriverDiff(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Diff: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Diff: %w", err)
	}
	spec, err := specFromArgs(args)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Diff: %w", err)
	}
	current := currentFromArgs(args)
	result, err := driver.Diff(context.Background(), spec, current)
	if err != nil {
		return nil, err
	}
	return diffResultToMap(result), nil
}

// invokeDriverScale decodes args and calls ResourceDriver.Scale.
func (m *doModuleInstance) invokeDriverScale(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.Scale: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.Scale: %w", err)
	}
	replicas := intArg(args, "replicas")
	out, err := driver.Scale(context.Background(), refFromArgs(args), replicas)
	if err != nil {
		return nil, err
	}
	return resourceOutputToMap(out), nil
}

// invokeDriverSensitiveKeys calls ResourceDriver.SensitiveKeys.
func (m *doModuleInstance) invokeDriverSensitiveKeys(args map[string]any) (map[string]any, error) {
	resourceType, _ := args["resource_type"].(string)
	if resourceType == "" {
		return nil, fmt.Errorf("ResourceDriver.SensitiveKeys: missing resource_type arg")
	}
	driver, err := m.provider.ResourceDriver(resourceType)
	if err != nil {
		return nil, fmt.Errorf("ResourceDriver.SensitiveKeys: %w", err)
	}
	return map[string]any{"keys": driver.SensitiveKeys()}, nil
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
	m := map[string]any{
		"provider_id": out.ProviderID,
		"name":        out.Name,
		"type":        out.Type,
		"status":      out.Status,
		"outputs":     out.Outputs,
	}
	if len(out.Sensitive) > 0 {
		m["sensitive"] = out.Sensitive
	}
	return m
}

// currentFromArgs decodes the "current_*" prefixed args into a *ResourceOutput
// for use in Diff calls. Returns nil if no current state is provided.
func currentFromArgs(args map[string]any) *interfaces.ResourceOutput {
	providerID, _ := args["current_provider_id"].(string)
	name, _ := args["current_name"].(string)
	typ, _ := args["current_type"].(string)
	status, _ := args["current_status"].(string)
	if providerID == "" && name == "" && typ == "" {
		return nil
	}
	out := &interfaces.ResourceOutput{
		ProviderID: providerID,
		Name:       name,
		Type:       typ,
		Status:     status,
	}
	if outputs, ok := args["current_outputs"].(map[string]any); ok {
		out.Outputs = outputs
	}
	switch v := args["current_sensitive"].(type) {
	case map[string]bool:
		out.Sensitive = v
	case map[string]any:
		// gRPC/protobuf Struct deserializes nested objects as map[string]any.
		sens := make(map[string]bool, len(v))
		for k, val := range v {
			if b, ok := val.(bool); ok {
				sens[k] = b
			}
		}
		if len(sens) > 0 {
			out.Sensitive = sens
		}
	}
	return out
}

// diffResultToMap converts a DiffResult into a map[string]any for transport.
func diffResultToMap(d *interfaces.DiffResult) map[string]any {
	if d == nil {
		return map[string]any{"needs_update": false, "needs_replace": false, "changes": []any{}}
	}
	changes := make([]any, len(d.Changes))
	for i, c := range d.Changes {
		changes[i] = map[string]any{
			"path":      c.Path,
			"old":       c.Old,
			"new":       c.New,
			"force_new": c.ForceNew,
		}
	}
	return map[string]any{
		"needs_update":  d.NeedsUpdate,
		"needs_replace": d.NeedsReplace,
		"changes":       changes,
	}
}

// intArg extracts an integer from args, tolerating both int and float64
// (JSON numbers unmarshal as float64 in map[string]any).
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// refsFromArgs decodes the "refs" arg into a []ResourceRef via JSON round-trip.
func refsFromArgs(args map[string]any) ([]interfaces.ResourceRef, error) {
	var refs []interfaces.ResourceRef
	if err := decodeJSONField(args, "refs", &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

// decodeJSONField marshals args[key] to JSON, then unmarshals into out.
func decodeJSONField(args map[string]any, key string, out any) error {
	v, ok := args[key]
	if !ok || v == nil {
		return nil // leave out at its zero value
	}
	return decodeJSONValue(v, out)
}

// decodeJSONValue marshals v to JSON, then unmarshals into out.
func decodeJSONValue(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}

// structToMap serialises v to JSON and back to map[string]any for transport.
func structToMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("structToMap marshal: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("structToMap unmarshal: %w", err)
	}
	return m, nil
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}
