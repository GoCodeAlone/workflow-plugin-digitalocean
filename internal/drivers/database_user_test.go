package drivers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/iac/jitsubst"
	"github.com/GoCodeAlone/workflow/iac/sensitive"
	"github.com/GoCodeAlone/workflow/iac/sensitiveinputs"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/secrets"
	"github.com/digitalocean/godo"
)

const databaseUserParent = "9cc10173-e9ea-4176-9dbc-a4cee4c4ff30"
const databaseUserSecret = "KNOWN_DATABASE_SECRET:/?#@%+"

type databaseUserAPI struct {
	mu       sync.Mutex
	username string
	parentID string
	password string
	host     string
	size     string
	status   map[string]int
	calls    map[string]int
}

func newDatabaseUserAPI(t *testing.T, username string) (*databaseUserAPI, *godo.Client) {
	t.Helper()
	f := &databaseUserAPI{username: username, parentID: databaseUserParent, password: databaseUserSecret, host: "db.example.test", size: "db-s-1vcpu-1gb", status: map[string]int{}, calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.EscapedPath()
		base := "/v2/databases/" + databaseUserParent
		operation := ""
		switch {
		case r.Method == http.MethodPost && path == "/v2/databases":
			operation = "cluster_create"
		case r.Method == http.MethodGet && path == base:
			operation = "parent"
		case r.Method == http.MethodPut && path == base+"/resize":
			operation = "resize"
		case r.Method == http.MethodPut && path == base+"/firewall":
			operation = "firewall"
		case r.Method == http.MethodPost && path == base+"/users/doadmin/reset_auth":
			operation = "admin_reset"
		case r.Method == http.MethodPost && path == base+"/users":
			operation = "create"
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("invalid create body")
			}
			if body["name"] != username || len(body) != 1 {
				t.Error("unexpected user create payload")
			}
		case path == base+"/users/"+url.PathEscape(username):
			switch r.Method {
			case http.MethodGet:
				operation = "read"
			case http.MethodDelete:
				operation = "delete"
			}
		case r.Method == http.MethodPost && path == base+"/users/"+url.PathEscape(username)+"/reset_auth":
			operation = "reset"
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Error("unexpected reset payload")
			}
		}
		if operation == "" {
			t.Errorf("unexpected API request %s %s", r.Method, path)
			w.WriteHeader(500)
			return
		}
		f.calls[operation]++
		w.Header().Set("Content-Type", "application/json")
		if status := f.status[operation]; status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "test_error", "message": databaseUserSecret})
			return
		}
		if operation == "delete" || operation == "resize" || operation == "firewall" {
			if operation == "resize" {
				var req godo.DatabaseResizeRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error("invalid resize payload")
				}
				f.size = req.SizeSlug
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if operation == "parent" || operation == "cluster_create" {
			_ = json.NewEncoder(w).Encode(map[string]any{"database": map[string]any{
				"id": f.parentID, "name": "db", "engine": "pg", "status": "online", "size": f.size,
				"connection": map[string]any{"host": f.host, "port": 25060, "database": "db/name?#", "user": "doadmin", "password": databaseUserSecret, "uri": "DO_NOT_COPY_ADMIN_URI", "ssl": true},
			}})
			return
		}
		name := f.username
		if operation == "admin_reset" {
			name = "doadmin"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"name": name, "role": "normal", "password": f.password}})
	}))
	t.Cleanup(srv.Close)
	return f, godoClientForTest(t, srv)
}

func (f *databaseUserAPI) count(operation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[operation]
}

func databaseUserSpec(username string) interfaces.ResourceSpec {
	return interfaces.ResourceSpec{Name: "app-user", Type: "digitalocean.database_user", Config: map[string]any{"database_id": databaseUserParent, "username": username, "rotation_epoch": "1"}}
}

func databaseUserRef(username string) interfaces.ResourceRef {
	return interfaces.ResourceRef{Name: "app-user", Type: "digitalocean.database_user", ProviderID: databaseUserParent + "/" + url.PathEscape(username)}
}

func databasePriorState(ref interfaces.ResourceRef, epochKey, epoch string) *interfaces.ResourceState {
	return &interfaces.ResourceState{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Outputs: map[string]any{
		epochKey: epoch, "password": sensitive.Placeholder(ref.Name, "password"), "uri": sensitive.Placeholder(ref.Name, "uri"),
	}}
}

func databaseOutputState(ref interfaces.ResourceRef, out *interfaces.ResourceOutput) *interfaces.ResourceState {
	return &interfaces.ResourceState{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Outputs: out.Outputs}
}

func updateDatabaseWithState(t *testing.T, driver interfaces.ResourceDriver, ref interfaces.ResourceRef, spec interfaces.ResourceSpec, prior *interfaces.ResourceState) (*interfaces.ResourceOutput, error) {
	t.Helper()
	updater, ok := driver.(interfaces.ResourceStateUpdater)
	if !ok {
		t.Fatal("driver does not implement public ResourceStateUpdater")
	}
	return updater.UpdateWithState(t.Context(), ref, spec, prior)
}

func TestDatabaseRotation_UpdateWithStateRestartAndStaleDiff(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, scenario := range []string{"restart-without-diff", "stale-diff-says-rotate", "stale-diff-says-noop"} {
			t.Run(strconv.FormatBool(admin)+"/"+scenario, func(t *testing.T) {
				f, client := newDatabaseUserAPI(t, "app-user")
				var driver interfaces.ResourceDriver = drivers.NewDatabaseUserDriver(client)
				ref, spec, key, reset := databaseUserRef("app-user"), databaseUserSpec("app-user"), "rotation_epoch", "reset"
				if admin {
					driver = drivers.NewDatabaseDriver(client, "nyc3")
					ref = interfaces.ResourceRef{Name: "db", Type: "infra.database", ProviderID: databaseUserParent}
					key, reset = "admin_rotation_epoch", "admin_reset"
					spec = interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{key: "2", "trusted_sources": []any{}}}
				}
				spec.Config[key] = "2"
				prior := databasePriorState(ref, key, "1")
				if scenario != "restart-without-diff" {
					staleEpoch := "2"
					if scenario == "stale-diff-says-rotate" {
						staleEpoch, prior.Outputs[key] = "1", "2"
					}
					if _, err := driver.Diff(t.Context(), spec, &interfaces.ResourceOutput{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Outputs: map[string]any{key: staleEpoch}}); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := json.Marshal(prior)
				out, err := updateDatabaseWithState(t, driver, ref, spec, prior)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if prior.Outputs[key] == "2" {
					want = 0
					if value, exists := out.Outputs["password"]; exists && !sensitive.IsPlaceholder(value) {
						t.Fatal("no-op update re-emitted credentials")
					}
				}
				if f.count(reset) != want || out.Outputs[key] != "2" {
					t.Fatalf("dispatch ignored persisted epoch: resets=%d, want=%d", f.count(reset), want)
				}
				after, _ := json.Marshal(prior)
				if string(before) != string(after) {
					t.Fatal("update mutated authoritative prior state")
				}
			})
		}
	}
}

func TestDatabaseRotation_UpdateWithStateRejectsUnboundPriorBeforeMutation(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, invalid := range []string{"nil", "name", "type", "provider-id", "spec-name", "spec-type", "epoch"} {
			t.Run(strconv.FormatBool(admin)+"/"+invalid, func(t *testing.T) {
				f, client := newDatabaseUserAPI(t, "app-user")
				var driver interfaces.ResourceDriver = drivers.NewDatabaseUserDriver(client)
				ref, spec, key := databaseUserRef("app-user"), databaseUserSpec("app-user"), "rotation_epoch"
				if admin {
					driver = drivers.NewDatabaseDriver(client, "nyc3")
					ref = interfaces.ResourceRef{Name: "db", Type: "infra.database", ProviderID: databaseUserParent}
					key = "admin_rotation_epoch"
					spec = interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{key: "2", "trusted_sources": []any{}}}
				}
				prior := databasePriorState(ref, key, "1")
				switch invalid {
				case "nil":
					prior = nil
				case "name":
					prior.Name = "other"
				case "type":
					prior.Type = "other"
				case "provider-id":
					prior.ProviderID = "other"
				case "spec-name":
					spec.Name = "other"
				case "spec-type":
					spec.Type = "other"
				case "epoch":
					prior.Outputs[key] = []any{databaseUserSecret}
				}
				_, err := updateDatabaseWithState(t, driver, ref, spec, prior)
				if !errors.Is(err, interfaces.ErrValidation) || strings.Contains(err.Error(), databaseUserSecret) {
					t.Fatalf("unbound prior state not rejected safely: %v", err)
				}
				if f.count("reset")+f.count("admin_reset")+f.count("resize")+f.count("firewall")+f.count("parent") != 0 {
					t.Fatal("invalid prior state reached API")
				}
			})
		}
	}
}

func TestDatabaseRotation_LegacyUpdateCannotUseDiffAsAuthority(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(strconv.FormatBool(admin), func(t *testing.T) {
			f, client := newDatabaseUserAPI(t, "app-user")
			var driver interfaces.ResourceDriver = drivers.NewDatabaseUserDriver(client)
			ref, spec, key := databaseUserRef("app-user"), databaseUserSpec("app-user"), "rotation_epoch"
			if admin {
				driver = drivers.NewDatabaseDriver(client, "nyc3")
				ref = interfaces.ResourceRef{Name: "db", Type: "infra.database", ProviderID: databaseUserParent}
				key = "admin_rotation_epoch"
				spec = interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{key: "2", "trusted_sources": []any{}}}
			}
			if _, err := driver.Diff(t.Context(), spec, &interfaces.ResourceOutput{ProviderID: ref.ProviderID, Outputs: map[string]any{key: "0"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Update(t.Context(), ref, spec); !errors.Is(err, interfaces.ErrValidation) {
				t.Fatalf("legacy explicit rotation used private authority: %v", err)
			}
			if f.count("reset")+f.count("admin_reset")+f.count("resize")+f.count("firewall") != 0 {
				t.Fatal("legacy rotation mutated API")
			}
			delete(spec.Config, key)
			if _, err := driver.Update(t.Context(), ref, spec); err != nil {
				t.Fatal(err)
			}
			if f.count("reset")+f.count("admin_reset") != 0 {
				t.Fatal("ordinary legacy update rotated credentials")
			}
		})
	}
}

func TestDatabaseRotation_FailedResetPreservesEpochAndSecretRefs(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(strconv.FormatBool(admin), func(t *testing.T) {
			f, client := newDatabaseUserAPI(t, "app-user")
			var driver interfaces.ResourceDriver = drivers.NewDatabaseUserDriver(client)
			ref, spec, key, reset := databaseUserRef("app-user"), databaseUserSpec("app-user"), "rotation_epoch", "reset"
			if admin {
				driver = drivers.NewDatabaseDriver(client, "nyc3")
				ref = interfaces.ResourceRef{Name: "db", Type: "infra.database", ProviderID: databaseUserParent}
				key, reset = "admin_rotation_epoch", "admin_reset"
				spec = interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{key: "2"}}
			}
			spec.Config[key] = "2"
			prior := databasePriorState(ref, key, "1")
			before, _ := json.Marshal(prior)
			f.status[reset] = http.StatusForbidden
			out, err := updateDatabaseWithState(t, driver, ref, spec, prior)
			if out != nil || !errors.Is(err, interfaces.ErrForbidden) || strings.Contains(err.Error(), databaseUserSecret) {
				t.Fatalf("failed reset exposed credentials or acknowledged epoch: %v", err)
			}
			after, _ := json.Marshal(prior)
			if string(before) != string(after) || strings.Contains(string(after), databaseUserSecret) {
				t.Fatal("failed reset changed prior epoch or secret references")
			}
			f.mu.Lock()
			delete(f.status, reset)
			f.mu.Unlock()
			out, err = updateDatabaseWithState(t, driver, ref, spec, prior)
			if err != nil || out.Outputs[key] != "2" || f.count(reset) != 2 {
				t.Fatalf("failed reset suppressed retry: %v", err)
			}
			store := secrets.NewFileProvider(t.TempDir())
			routed, _, err := sensitive.Route(t.Context(), store, out.Name, out)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(routed)
			if strings.Contains(string(encoded), databaseUserSecret) || !sensitive.IsPlaceholder(routed["password"]) || !sensitive.IsPlaceholder(routed["uri"]) {
				t.Fatal("successful retry did not route sensitive outputs")
			}
		})
	}
}

func TestDatabaseRotation_NoResetPreservesRoutedReferencesWithoutRewritingSecrets(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, epochPresent := range []bool{false, true} {
			t.Run(strconv.FormatBool(admin)+"/"+strconv.FormatBool(epochPresent), func(t *testing.T) {
				f, client := newDatabaseUserAPI(t, "app-user")
				var driver interfaces.ResourceDriver = drivers.NewDatabaseUserDriver(client)
				ref, spec, key := databaseUserRef("app-user"), databaseUserSpec("app-user"), "rotation_epoch"
				if admin {
					driver = drivers.NewDatabaseDriver(client, "nyc3")
					ref = interfaces.ResourceRef{Name: "db", Type: "infra.database", ProviderID: databaseUserParent}
					key = "admin_rotation_epoch"
					spec = interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{key: "1"}}
				}
				if !epochPresent {
					delete(spec.Config, key)
				}
				prior := databasePriorState(ref, key, "1")
				store := secrets.NewFileProvider(t.TempDir())
				for _, key := range []string{"password", "uri"} {
					if err := store.Set(t.Context(), sensitive.SecretKey(ref.Name, key), databaseUserSecret); err != nil {
						t.Fatal(err)
					}
				}
				out, err := updateDatabaseWithState(t, driver, ref, spec, prior)
				if err != nil {
					t.Fatal(err)
				}
				if out.Outputs[key] != "1" || out.Outputs["password"] != prior.Outputs["password"] || out.Outputs["uri"] != prior.Outputs["uri"] {
					t.Fatal("state-aware non-rotation update dropped persisted epoch or routed references")
				}
				routed, hydrated, err := sensitive.Route(t.Context(), store, ref.Name, out)
				if err != nil || len(hydrated) != 0 || routed["password"] != prior.Outputs["password"] {
					t.Fatalf("non-rotation update re-routed existing references: %v", err)
				}
				for _, key := range []string{"password", "uri"} {
					value, err := store.Get(t.Context(), sensitive.SecretKey(ref.Name, key))
					if err != nil || value != databaseUserSecret {
						t.Fatal("non-rotation update overwrote stored credentials with reference text")
					}
				}
				if f.count("reset")+f.count("admin_reset") != 0 {
					t.Fatal("non-rotation update reset credentials")
				}
			})
		}
	}
}

func TestDatabaseUser_CreateTLSAndEscapedIdentity(t *testing.T) {
	username := "app/+?#%:@ user"
	f, client := newDatabaseUserAPI(t, username)
	d := drivers.NewDatabaseUserDriver(client)
	out, err := d.Create(t.Context(), databaseUserSpec(username))
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "app-user" || out.Type != "digitalocean.database_user" || out.ProviderID != databaseUserRef(username).ProviderID || d.ProviderIDFormat() != interfaces.IDFormatFreeform {
		t.Fatal("incorrect parent-bound identity")
	}
	if out.Outputs["database_id"] != databaseUserParent || out.Outputs["username"] != username || out.Outputs["rotation_epoch"] != "1" {
		t.Fatal("missing identity/epoch metadata")
	}
	if out.Outputs["password"] != databaseUserSecret || !out.Sensitive["uri"] || !out.Sensitive["password"] {
		t.Fatal("credential routing missing")
	}
	u, err := url.Parse(out.Outputs["uri"].(string))
	if err != nil {
		t.Fatal("URI not parseable")
	}
	password, _ := u.User.Password()
	if password != databaseUserSecret || u.User.Username() != username || u.Host != "db.example.test:25060" || u.Path != "/db/name?#" || u.EscapedPath() != "/db%2Fname%3F%23" || u.Query().Get("sslmode") != "require" {
		t.Fatal("URI escaping/TLS/credentials incorrect")
	}
	if f.count("parent") != 1 || f.count("create") != 1 || f.count("reset") != 0 {
		t.Fatal("unexpected create API operations")
	}
}

func TestDatabaseUser_ReadOmitsCredentialsAndEpoch(t *testing.T) {
	username := "app/+?#%:@ user"
	_, client := newDatabaseUserAPI(t, username)
	d := drivers.NewDatabaseUserDriver(client)
	out, err := d.Read(t.Context(), databaseUserRef(username))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"uri", "password", "rotation_epoch"} {
		if _, ok := out.Outputs[key]; ok {
			t.Fatalf("refresh overwrites persisted %s", key)
		}
	}
	if out.ProviderID != databaseUserRef(username).ProviderID || out.Outputs["username"] != username {
		t.Fatal("read identity mismatch")
	}
}

func TestDatabaseUser_ExplicitEpochRotationOnceAndReload(t *testing.T) {
	f, client := newDatabaseUserAPI(t, "app-user")
	d := drivers.NewDatabaseUserDriver(client)
	spec := databaseUserSpec("app-user")
	out, err := d.Create(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Config["rotation_epoch"] = "2"
	diff, err := d.Diff(t.Context(), spec, out)
	if err != nil || !diff.NeedsUpdate || diff.NeedsReplace {
		t.Fatalf("epoch diff = %v, %v", diff, err)
	}
	rotated, err := d.UpdateWithState(t.Context(), databaseUserRef("app-user"), spec, databaseOutputState(databaseUserRef("app-user"), out))
	if err != nil {
		t.Fatal(err)
	}
	if f.count("reset") != 1 || rotated.Outputs["rotation_epoch"] != "2" || rotated.Outputs["password"] != databaseUserSecret {
		t.Fatal("explicit rotation missing")
	}
	if _, err := d.UpdateWithState(t.Context(), databaseUserRef("app-user"), spec, databaseOutputState(databaseUserRef("app-user"), rotated)); err != nil {
		t.Fatal(err)
	}
	if f.count("reset") != 1 {
		t.Fatal("same epoch reset repeatedly")
	}
	store := secrets.NewFileProvider(t.TempDir())
	sanitized, _, err := sensitive.Route(t.Context(), store, rotated.Name, rotated)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := *rotated
	snapshot.Outputs = sanitized
	encoded, err := json.Marshal(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), databaseUserSecret) {
		t.Fatal("routed state contains known secret")
	}
	var restored interfaces.ResourceOutput
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	fresh := drivers.NewDatabaseUserDriver(client)
	diff, err = fresh.Diff(t.Context(), spec, &restored)
	if err != nil || diff.NeedsUpdate {
		t.Fatalf("persisted epoch not respected: %v, %v", diff, err)
	}
	noRotation, err := fresh.UpdateWithState(t.Context(), databaseUserRef("app-user"), spec, databaseOutputState(databaseUserRef("app-user"), &restored))
	if err != nil {
		t.Fatal(err)
	}
	if f.count("reset") != 1 {
		t.Fatal("restart repeated rotation")
	}
	if value, ok := noRotation.Outputs["password"]; ok && !sensitive.IsPlaceholder(value) {
		t.Fatal("no-op re-emitted password")
	}
	key := sensitive.SecretKey(rotated.Name, "password")
	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	consumer := interfaces.ResourceSpec{Config: map[string]any{"password": restored.Outputs["password"]}}
	lookup := func(key string) (string, error) { return store.Get(t.Context(), key) }
	_, err = jitsubst.ResolveSpecWithSecretLookup(consumer, nil, nil, nil, lookup)
	if !errors.Is(err, secrets.ErrNotFound) || strings.Contains(err.Error(), databaseUserSecret) {
		t.Fatal("missing routed credential did not fail closed")
	}
	if f.count("reset") != 1 {
		t.Fatal("missing routed credential caused implicit rotation")
	}
	spec.Config["rotation_epoch"] = "3"
	if _, err := fresh.Diff(t.Context(), spec, &restored); err != nil {
		t.Fatal(err)
	}
	recovered, err := fresh.UpdateWithState(t.Context(), databaseUserRef("app-user"), spec, databaseOutputState(databaseUserRef("app-user"), &restored))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sensitive.Route(t.Context(), store, recovered.Name, recovered); err != nil {
		t.Fatal(err)
	}
	if _, err := jitsubst.ResolveSpecWithSecretLookup(consumer, nil, nil, nil, lookup); err != nil {
		t.Fatal(err)
	}
	if f.count("reset") != 2 {
		t.Fatal("explicit recovery epoch did not rotate once")
	}
}

func TestDatabaseUser_CancellationAndFailedResetDoNotAcknowledgeEpoch(t *testing.T) {
	f, client := newDatabaseUserAPI(t, "app-user")
	d := drivers.NewDatabaseUserDriver(client)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.Create(ctx, databaseUserSpec("app-user")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
	if f.count("create") != 0 {
		t.Fatal("canceled operation mutated user")
	}
	current := &interfaces.ResourceOutput{ProviderID: databaseUserRef("app-user").ProviderID, Outputs: map[string]any{"rotation_epoch": "0"}}
	if _, err := d.Diff(t.Context(), databaseUserSpec("app-user"), current); err != nil {
		t.Fatal(err)
	}
	f.password = ""
	if _, err := d.UpdateWithState(t.Context(), databaseUserRef("app-user"), databaseUserSpec("app-user"), databaseOutputState(databaseUserRef("app-user"), current)); err == nil {
		t.Fatal("incomplete reset acknowledged epoch")
	}
	f.mu.Lock()
	f.password = databaseUserSecret
	f.mu.Unlock()
	if _, err := d.UpdateWithState(t.Context(), databaseUserRef("app-user"), databaseUserSpec("app-user"), databaseOutputState(databaseUserRef("app-user"), current)); err != nil {
		t.Fatal(err)
	}
	if f.count("reset") != 2 {
		t.Fatal("incomplete reset falsely acknowledged epoch")
	}
}

func TestDatabaseUser_MalformedConfigAndIdentityBeforeAPI(t *testing.T) {
	f, client := newDatabaseUserAPI(t, "app-user")
	d := drivers.NewDatabaseUserDriver(client)
	for _, change := range []struct {
		key   string
		value any
	}{
		{"database_id", ""}, {"database_id", "not-uuid"}, {"database_id", "9zz10173-e9ea-4176-9dbc-a4cee4c4ff30"}, {"database_id", 1},
		{"username", ""}, {"username", true}, {"username", "\x00"}, {"rotation_epoch", ""}, {"rotation_epoch", 2},
	} {
		spec := databaseUserSpec("app-user")
		spec.Config[change.key] = change.value
		if _, err := d.Create(t.Context(), spec); err == nil {
			t.Fatalf("accepted malformed %s", change.key)
		}
	}
	for _, id := range []string{"", databaseUserParent, databaseUserParent + "/", "bad/user", databaseUserParent + "/user/extra", databaseUserParent + "/%ZZ", databaseUserParent + "/%61pp-user", databaseUserParent + "/a%2fb", databaseUserParent + "/%00"} {
		if _, err := d.Read(t.Context(), interfaces.ResourceRef{ProviderID: id}); err == nil {
			t.Fatal("accepted malformed provider ID")
		}
	}
	if f.count("parent") != 0 || f.count("create") != 0 || f.count("read") != 0 {
		t.Fatal("malformed input reached API")
	}
}

func TestDatabaseUser_IdentityMismatchStopsMutation(t *testing.T) {
	for _, mismatch := range []string{"parent", "user", "desired"} {
		t.Run(mismatch, func(t *testing.T) {
			f, client := newDatabaseUserAPI(t, "app-user")
			d := drivers.NewDatabaseUserDriver(client)
			spec := databaseUserSpec("app-user")
			current := &interfaces.ResourceOutput{ProviderID: databaseUserRef("app-user").ProviderID, Outputs: map[string]any{"rotation_epoch": "0"}}
			if _, err := d.Diff(t.Context(), spec, current); err != nil {
				t.Fatal(err)
			}
			switch mismatch {
			case "parent":
				f.parentID = "bbd10173-e9ea-4176-9dbc-a4cee4c4ff30"
			case "user":
				f.username = "other"
			case "desired":
				spec.Config["database_id"] = "bbd10173-e9ea-4176-9dbc-a4cee4c4ff30"
			}
			if _, err := d.UpdateWithState(t.Context(), databaseUserRef("app-user"), spec, databaseOutputState(databaseUserRef("app-user"), current)); err == nil {
				t.Fatal("identity mismatch accepted")
			}
			if f.count("reset") != 0 {
				t.Fatal("identity mismatch mutated user")
			}
		})
	}
}

func TestDatabaseUser_APIErrorClassificationAndSafeDiagnostics(t *testing.T) {
	for _, operation := range []string{"parent", "create", "read", "reset", "delete"} {
		for _, tc := range []struct {
			status   int
			sentinel error
		}{{404, interfaces.ErrResourceNotFound}, {409, interfaces.ErrResourceAlreadyExists}, {403, interfaces.ErrForbidden}, {429, interfaces.ErrRateLimited}, {500, interfaces.ErrTransient}} {
			t.Run(operation+"/"+http.StatusText(tc.status), func(t *testing.T) {
				f, client := newDatabaseUserAPI(t, "app-user")
				f.status[operation] = tc.status
				d := drivers.NewDatabaseUserDriver(client)
				spec, ref := databaseUserSpec("app-user"), databaseUserRef("app-user")
				var err error
				switch operation {
				case "parent", "create":
					_, err = d.Create(t.Context(), spec)
				case "read":
					_, err = d.Read(t.Context(), ref)
				case "reset":
					_, err = d.Diff(t.Context(), spec, &interfaces.ResourceOutput{ProviderID: ref.ProviderID, Outputs: map[string]any{}})
					if err != nil {
						t.Fatal(err)
					}
					_, err = d.UpdateWithState(t.Context(), ref, spec, &interfaces.ResourceState{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Outputs: map[string]any{}})
				case "delete":
					err = d.Delete(t.Context(), ref)
				}
				if operation == "delete" && tc.status == 404 {
					if err != nil {
						t.Fatal("delete 404 not idempotent")
					}
					return
				}
				if !errors.Is(err, tc.sentinel) {
					t.Fatalf("lost API classification: %v", err)
				}
				if strings.Contains(err.Error(), databaseUserSecret) {
					t.Fatal("error leaked API source bytes")
				}
				var cause *godo.ErrorResponse
				if !errors.As(err, &cause) {
					t.Fatal("API cause lost")
				}
			})
		}
	}
}

func TestDatabaseUser_MissingCredentialOrInvalidHostFailsSafely(t *testing.T) {
	for _, invalid := range []string{"password", "host"} {
		t.Run(invalid, func(t *testing.T) {
			f, client := newDatabaseUserAPI(t, "app-user")
			if invalid == "password" {
				f.password = ""
			} else {
				f.host = "https://" + databaseUserSecret
			}
			_, err := drivers.NewDatabaseUserDriver(client).Create(t.Context(), databaseUserSpec("app-user"))
			if err == nil || strings.Contains(err.Error(), databaseUserSecret) {
				t.Fatal("incomplete credential/endpoint accepted or disclosed")
			}
			if invalid == "host" && f.count("create") != 0 {
				t.Fatal("invalid endpoint passed mutation preflight")
			}
		})
	}
}

func TestDatabaseUser_DeleteIdempotentAndUnseededRotationFails(t *testing.T) {
	f, client := newDatabaseUserAPI(t, "app-user")
	d := drivers.NewDatabaseUserDriver(client)
	if _, err := d.Update(t.Context(), databaseUserRef("app-user"), databaseUserSpec("app-user")); err == nil {
		t.Fatal("unseeded epoch rotation accepted")
	}
	if f.count("reset") != 0 {
		t.Fatal("unseeded rotation reached API")
	}
	if err := d.Delete(t.Context(), databaseUserRef("app-user")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status["delete"] = 404
	f.mu.Unlock()
	if err := d.Delete(t.Context(), databaseUserRef("app-user")); err != nil {
		t.Fatal(err)
	}
	if f.count("delete") != 2 {
		t.Fatal("delete was not parent-bound")
	}
}

func TestDatabaseUser_DiffIdentityReplacementAndSensitiveInputs(t *testing.T) {
	_, client := newDatabaseUserAPI(t, "app-user")
	d := drivers.NewDatabaseUserDriver(client)
	current := &interfaces.ResourceOutput{ProviderID: databaseUserRef("app-user").ProviderID, Outputs: map[string]any{"rotation_epoch": "1"}}
	spec := databaseUserSpec("other")
	diff, err := d.Diff(t.Context(), spec, current)
	if err != nil || !diff.NeedsReplace {
		t.Fatalf("identity rename did not replace: %v, %v", diff, err)
	}
	for _, driver := range []interfaces.ResourceSensitiveInputDeclarer{d, drivers.NewDatabaseDriver(client, "nyc3")} {
		paths, err := driver.SensitiveInputPaths(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"password", "uri"} {
			err := sensitiveinputs.ValidateReferences(map[string]any{key: databaseUserSecret}, paths)
			if err == nil || strings.Contains(err.Error(), databaseUserSecret) {
				t.Fatal("literal credential accepted or disclosed")
			}
			if err := sensitiveinputs.ValidateReferences(map[string]any{key: "secret_ref://named/credential"}, paths); err != nil {
				t.Fatal(err)
			}
		}
	}
}
