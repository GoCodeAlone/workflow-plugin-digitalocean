package drivers

import "github.com/digitalocean/godo"

// AppOutputForTest exports appOutput for in-package test assertions.
// drivers_test package cannot reach appOutput directly because it's
// unexported; this file lives alongside the package and only compiles
// in `go test` mode (via the _test.go suffix), so it doesn't bloat the
// production binary.
func AppOutputForTest(app *godo.App) map[string]any {
	if r := appOutput(app); r != nil {
		return r.Outputs
	}
	return nil
}
