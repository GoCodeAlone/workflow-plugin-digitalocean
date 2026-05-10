package internal

// dispatcher_coverage_test.go closes the CI gap that allowed the
// IaCProvider.EnumerateAll dispatch case to be missed during the v0.14.0
// EnumeratorAll satisfaction work.
//
// Background
// ──────────
// The DO plugin satisfies optional IaCProvider sub-interfaces (Enumerator,
// EnumeratorAll, ProviderMigrationRepairer, ProviderCredentialRevoker) at the
// Go-type level via methods on *DOProvider. The plugin/host gRPC boundary
// does NOT discover these methods reflectively — it routes calls through
// doModuleInstance.InvokeMethod, a hardcoded switch keyed on method-name
// strings ("IaCProvider.EnumerateAll"). When a new optional interface is
// added to *DOProvider but the corresponding switch case is forgotten, wfctl
// receives "unknown method" at runtime against staging, bypassing every
// compile-time check.
//
// Per the user's strict-mode mandate ("force strict, no fallbacks"), this
// regression class must be caught at CI time. The wfctl-side
// remoteIaCProvider only validates the Go-type-level bridge; it has no way
// to introspect plugin-side dispatch coverage. This test does that
// introspection from the plugin side via reflection over the IaCProvider
// optional sub-interfaces *DOProvider claims to satisfy.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// optionalProviderInterfaces enumerates every IaCProvider sub-interface
// upstream workflow defines that the DO plugin's *DOProvider may satisfy.
// New optional interfaces added in workflow MUST be appended here so this
// test continues to enforce dispatcher coverage. Forgetting to add an entry
// silently weakens the gate; consider that part of the upstream-interface
// adoption checklist.
//
// Includes both IaCProvider sub-interfaces (provider methods) and
// ResourceDriver sub-interfaces (driver methods). The same dispatcher in
// module_instance.go routes both, so coverage must extend to both.
func optionalProviderInterfaces() []reflect.Type {
	return []reflect.Type{
		// IaCProvider optional sub-interfaces.
		reflect.TypeOf((*interfaces.Enumerator)(nil)).Elem(),
		reflect.TypeOf((*interfaces.EnumeratorAll)(nil)).Elem(),
		reflect.TypeOf((*interfaces.ProviderMigrationRepairer)(nil)).Elem(),
		reflect.TypeOf((*interfaces.ProviderCredentialRevoker)(nil)).Elem(),
		reflect.TypeOf((*interfaces.DriftConfigDetector)(nil)).Elem(),
		reflect.TypeOf((*interfaces.ProviderValidator)(nil)).Elem(),
	}
}

// methodSurfacePrefixes maps a reflected interface to the method-name prefix
// used by the dispatcher switch. IaCProvider sub-interfaces use the
// "IaCProvider." prefix; ResourceDriver sub-interfaces use "ResourceDriver.".
// Currently every entry is "IaCProvider." because the listed optional
// interfaces are all provider-scoped. Driver-scoped entries (Troubleshooter,
// ResourceReplacer, etc.) are dispatched on the driver returned by
// ResourceDriver(type), not on the provider directly, so they don't fit this
// switch-case pattern; they're covered by the per-driver test surface.
const providerMethodPrefix = "IaCProvider."

// dispatcherExceptions lists method names whose *interface name* is
// IaCProvider sub-interface but whose corresponding switch case intentionally
// does NOT exist in InvokeMethod because the host calls a different code
// path (e.g. DetectDriftWithSpecs is dispatched via "IaCProvider.DetectDrift"
// + a "specs" arg in the args map, not via a separate
// "IaCProvider.DetectDriftWithSpecs" case).
//
// Adding to this list is a deliberate, audited weakening of the gate. Each
// entry MUST include a comment explaining why no separate case is required,
// and reference the alternate dispatch site so reviewers can verify.
var dispatcherExceptions = map[string]string{
	// DriftConfigDetector.DetectDriftWithSpecs is dispatched via the existing
	// "IaCProvider.DetectDrift" case in module_instance.go: the dispatch reads
	// args["specs"] and, when present, type-asserts the provider against
	// specsDetector and routes to DetectDriftWithSpecs. See
	// invokeProviderDetectDrift.
	"DetectDriftWithSpecs": "dispatched via IaCProvider.DetectDrift + specs arg (see invokeProviderDetectDrift)",

	// ProviderValidator.ValidatePlan is invoked in-process by wfctl's plan
	// pipeline, not via the gRPC InvokeMethod boundary. The DO plugin's
	// validate_plan.go satisfies the interface for in-process callers; no
	// remote dispatch case is needed because wfctl never crosses the gRPC
	// boundary for this method. Revisit if the workflow planner adopts a
	// remote-validate dispatch.
	"ValidatePlan": "in-process only; wfctl planner does not cross the gRPC boundary",
}

// TestDispatcherCoversEveryProviderInterfaceMethod asserts: for every method
// declared by an optionalProviderInterfaces type that *DOProvider satisfies,
// internal/module_instance.go MUST have a corresponding
// `case "IaCProvider.<Method>":` in its InvokeMethod switch — unless the
// method is in dispatcherExceptions with a documented reason.
//
// Without this assertion, plugin-side dispatcher coverage is invisible to
// CI and only surfaces at runtime against live cloud (the bug that motivated
// this test: IaCProvider.EnumerateAll case missing in v0.14.0; wfctl received
// "unknown method" against staging).
//
// Methodology: parse module_instance.go via go/ast and walk every CaseClause
// in the file, collecting the string literals that key each case. AST
// parsing (not raw string scanning) is required because raw scanning matches
// commented-out cases — the original implementation passed even when the
// EnumerateAll case was commented out, which would have masked the bug this
// test exists to catch.
func TestDispatcherCoversEveryProviderInterfaceMethod(t *testing.T) {
	caseLiterals, err := collectDispatcherCaseLiterals(filepath.Join(".", "module_instance.go"))
	if err != nil {
		t.Fatalf("collectDispatcherCaseLiterals: %v", err)
	}

	// reflect on *DOProvider concrete type so the coverage gate is anchored
	// to the actual production provider, not a fake.
	providerType := reflect.TypeOf((*DOProvider)(nil))

	var failures []string
	for _, iface := range optionalProviderInterfaces() {
		// Skip if *DOProvider doesn't satisfy this interface — that's an
		// intentional "we don't implement this yet" stance, not a bug.
		if !providerType.Implements(iface) {
			t.Logf("note: *DOProvider does not satisfy %s — skipping coverage check for its methods",
				iface.String())
			continue
		}
		for i := 0; i < iface.NumMethod(); i++ {
			method := iface.Method(i)
			if reason, excepted := dispatcherExceptions[method.Name]; excepted {
				t.Logf("note: %s.%s is in dispatcherExceptions — %s",
					iface.String(), method.Name, reason)
				continue
			}
			expected := providerMethodPrefix + method.Name
			if !caseLiterals[expected] {
				failures = append(failures, formatMissingCase(iface, method.Name, `case "`+expected+`":`))
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("module_instance.go missing dispatcher cases:\n\n%s\n",
			strings.Join(failures, "\n\n"))
	}
}

// collectDispatcherCaseLiterals parses path as a Go source file and returns
// a set of string literals used as case keys in any CaseClause, including
// nested switches. Comments are not parsed as case clauses, so commented-out
// cases are correctly excluded.
//
// Returns the set as a map[string]bool with `true` for present literals so
// callers can use a one-liner membership check. Empty literals (the
// `default:` clause has no expressions) are not in the set.
func collectDispatcherCaseLiterals(path string) (map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	literals := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range cc.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			// lit.Value includes surrounding quotes — strip them.
			s := lit.Value
			if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
				s = s[1 : len(s)-1]
			}
			literals[s] = true
		}
		return true
	})
	return literals, nil
}

// formatMissingCase returns a multi-line, copy-pasteable error message that
// names the missing interface method, the runtime symptom (wfctl error
// string), and the exact line to add.
func formatMissingCase(iface reflect.Type, method, caseStmt string) string {
	return strings.Join([]string{
		"  " + iface.String() + "." + method + ":",
		"    *DOProvider satisfies " + iface.String() + " at the Go-type level,",
		"    but the gRPC dispatch switch in module_instance.go has no case for this method.",
		"    wfctl will receive 'unknown method \"IaCProvider." + method + "\"' at runtime",
		"    once it dispatches this call across the plugin boundary.",
		"",
		"    Add to InvokeMethodContext switch:",
		"        " + caseStmt,
		"            return m.invokeProvider" + method + "(ctx, args)",
		"",
		"    Then implement invokeProvider" + method + " in the same file,",
		"    mirroring the pattern of invokeProviderEnumerateByTag or invokeProviderRevokeCredential",
		"    (codes.Unimplemented when the provider doesn't satisfy the interface,",
		"    structToMap for the response).",
	}, "\n")
}
