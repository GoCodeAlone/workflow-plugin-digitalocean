package drivers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GoCodeAlone/workflow/iac/sensitive"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"github.com/google/uuid"
)

// DatabaseUserClient is the parent-bound subset of godo.DatabasesService used
// by database users. User path arguments are escaped before SDK dispatch.
type DatabaseUserClient interface {
	Get(context.Context, string) (*godo.Database, *godo.Response, error)
	CreateUser(context.Context, string, *godo.DatabaseCreateUserRequest) (*godo.DatabaseUser, *godo.Response, error)
	GetUser(context.Context, string, string) (*godo.DatabaseUser, *godo.Response, error)
	ResetUserAuth(context.Context, string, string, *godo.DatabaseResetUserAuthRequest) (*godo.DatabaseUser, *godo.Response, error)
	DeleteUser(context.Context, string, string) (*godo.Response, error)
}

type DatabaseUserDriver struct {
	client DatabaseUserClient
}

var _ interfaces.ResourceDriver = (*DatabaseUserDriver)(nil)
var _ interfaces.ResourceSensitiveInputDeclarer = (*DatabaseUserDriver)(nil)
var _ interfaces.ResourceStateUpdater = (*DatabaseUserDriver)(nil)

func NewDatabaseUserDriver(c *godo.Client) *DatabaseUserDriver {
	return NewDatabaseUserDriverWithClient(c.Databases)
}

func NewDatabaseUserDriverWithClient(c DatabaseUserClient) *DatabaseUserDriver {
	return &DatabaseUserDriver{client: c}
}

func (d *DatabaseUserDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	cfg, err := parseDatabaseUserConfig(spec.Config)
	if err != nil {
		return nil, err
	}
	db, err := readDatabaseParent(ctx, d.client, cfg.databaseID)
	if err != nil {
		return nil, err
	}
	if _, err := databaseTLSURI(db, cfg.username, "preflight"); err != nil {
		return nil, err
	}
	user, _, err := d.client.CreateUser(ctx, cfg.databaseID, &godo.DatabaseCreateUserRequest{Name: cfg.username})
	if err != nil {
		return nil, databaseAPIError("database user create", err)
	}
	out, err := databaseUserOutput(spec.Name, cfg.databaseID, cfg.username, db, user, true)
	if err != nil {
		return nil, err
	}
	if cfg.epochPresent {
		out.Outputs["rotation_epoch"] = cfg.epoch
	}
	return out, nil
}

func (d *DatabaseUserDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	id, username, err := parseDatabaseUserProviderID(ref.ProviderID)
	if err != nil {
		return nil, err
	}
	db, err := readDatabaseParent(ctx, d.client, id)
	if err != nil {
		return nil, err
	}
	user, _, err := d.client.GetUser(ctx, id, url.PathEscape(username))
	if err != nil {
		return nil, databaseAPIError("database user read", err)
	}
	// Omitting credentials and epoch preserves Workflow's routed references
	// and provider-local epoch metadata during refresh, even after a restart.
	return databaseUserOutput(ref.Name, id, username, db, user, false)
}

func (d *DatabaseUserDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return d.UpdateWithState(ctx, ref, spec, nil)
}

func (d *DatabaseUserDriver) UpdateWithState(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec, prior *interfaces.ResourceState) (*interfaces.ResourceOutput, error) {
	cfg, err := parseDatabaseUserConfig(spec.Config)
	if err != nil {
		return nil, err
	}
	if prior != nil || cfg.epochPresent {
		if err := interfaces.ValidateUpdatePriorState(ref, spec, prior); err != nil {
			return nil, err
		}
	}
	id, username, err := parseDatabaseUserProviderID(ref.ProviderID)
	if err != nil {
		return nil, err
	}
	if id != cfg.databaseID || username != cfg.username {
		return nil, databaseValidationError("database user update requires unchanged parent and username")
	}
	if !cfg.epochPresent {
		out, err := d.Read(ctx, ref)
		if err == nil {
			preserveDatabaseState(out, prior, "rotation_epoch")
		}
		return out, err
	}
	previous, _, err := databaseEpochFromConfig(prior.Outputs, "rotation_epoch")
	if err != nil {
		return nil, err
	}
	if previous == cfg.epoch {
		out, err := d.Read(ctx, ref)
		if err == nil {
			out.Outputs["rotation_epoch"] = cfg.epoch
			preserveDatabaseState(out, prior, "rotation_epoch")
		}
		return out, err
	}
	db, err := readDatabaseParent(ctx, d.client, id)
	if err != nil {
		return nil, err
	}
	if _, err := databaseTLSURI(db, username, "preflight"); err != nil {
		return nil, err
	}
	user, _, err := d.client.GetUser(ctx, id, url.PathEscape(username))
	if err != nil {
		return nil, databaseAPIError("database user rotation read", err)
	}
	if err := validateDatabaseUser(user, username); err != nil {
		return nil, err
	}
	user, _, err = d.client.ResetUserAuth(ctx, id, url.PathEscape(username), &godo.DatabaseResetUserAuthRequest{})
	if err != nil {
		return nil, databaseAPIError("database user reset auth", err)
	}
	out, err := databaseUserOutput(spec.Name, id, username, db, user, true)
	if err != nil {
		return nil, err
	}
	out.Outputs["rotation_epoch"] = cfg.epoch
	return out, nil
}

func (d *DatabaseUserDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	id, username, err := parseDatabaseUserProviderID(ref.ProviderID)
	if err != nil {
		return err
	}
	_, err = d.client.DeleteUser(ctx, id, url.PathEscape(username))
	if err != nil {
		var response *godo.ErrorResponse
		if errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == http.StatusNotFound {
			return nil
		}
		return databaseAPIError("database user delete", err)
	}
	return nil
}

func (d *DatabaseUserDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	cfg, err := parseDatabaseUserConfig(desired.Config)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	id, username, err := parseDatabaseUserProviderID(current.ProviderID)
	if err != nil {
		return nil, err
	}
	var changes []interfaces.FieldChange
	if id != cfg.databaseID {
		changes = append(changes, interfaces.FieldChange{Path: "database_id", Old: id, New: cfg.databaseID, ForceNew: true})
	}
	if username != cfg.username {
		changes = append(changes, interfaces.FieldChange{Path: "username", Old: username, New: cfg.username, ForceNew: true})
	}
	if cfg.epochPresent {
		previous, _, err := databaseEpochFromConfig(current.Outputs, "rotation_epoch")
		if err != nil {
			return nil, err
		}
		if previous != cfg.epoch {
			changes = append(changes, interfaces.FieldChange{Path: "rotation_epoch", Old: previous, New: cfg.epoch})
		}
	}
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, NeedsReplace: hasForceNewChange(changes), Changes: changes}, nil
}

func (d *DatabaseUserDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	_, err := d.Read(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, err
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

func (d *DatabaseUserDriver) Scale(context.Context, interfaces.ResourceRef, int) (*interfaces.ResourceOutput, error) {
	return nil, errors.New("database user does not support scale")
}

func (d *DatabaseUserDriver) SensitiveKeys() []string { return []string{"password", "uri"} }
func (d *DatabaseUserDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatFreeform
}
func (d *DatabaseUserDriver) SensitiveInputPaths(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []string{"/password", "/uri"}, nil
}

type databaseUserConfig struct {
	databaseID   string
	username     string
	epoch        string
	epochPresent bool
}

func parseDatabaseUserConfig(config map[string]any) (databaseUserConfig, error) {
	id, ok := config["database_id"].(string)
	if !ok || !canonicalDatabaseID(id) {
		return databaseUserConfig{}, databaseValidationError("database_id must be a canonical database UUID")
	}
	username, ok := config["username"].(string)
	if !ok || !validDatabaseUsername(username) {
		return databaseUserConfig{}, databaseValidationError("username must be a nonempty database username")
	}
	epoch, present, err := databaseEpochFromConfig(config, "rotation_epoch")
	if err != nil {
		return databaseUserConfig{}, err
	}
	return databaseUserConfig{databaseID: id, username: username, epoch: epoch, epochPresent: present}, nil
}

func canonicalDatabaseID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func validDatabaseUsername(username string) bool {
	if strings.TrimSpace(username) == "" || username == "." || username == ".." || !utf8.ValidString(username) {
		return false
	}
	return !strings.ContainsFunc(username, unicode.IsControl)
}

func parseDatabaseUserProviderID(providerID string) (string, string, error) {
	id, encoded, ok := strings.Cut(providerID, "/")
	if !ok || !canonicalDatabaseID(id) || strings.Contains(encoded, "/") {
		return "", "", databaseValidationError("provider_id must be <database-uuid>/<url-escaped-user>")
	}
	username, err := url.PathUnescape(encoded)
	if err != nil || !validDatabaseUsername(username) || url.PathEscape(username) != encoded {
		return "", "", databaseValidationError("provider_id must contain a canonical escaped username")
	}
	return id, username, nil
}

func validateDatabaseUser(user *godo.DatabaseUser, username string) error {
	if user == nil || user.Name != username {
		return databaseValidationError("database API returned incomplete or mismatched user identity")
	}
	return nil
}

func readDatabaseParent(ctx context.Context, client interface {
	Get(context.Context, string) (*godo.Database, *godo.Response, error)
}, id string) (*godo.Database, error) {
	db, _, err := client.Get(ctx, id)
	if err != nil {
		return nil, databaseAPIError("database parent read", err)
	}
	if db == nil || db.ID != id {
		return nil, databaseValidationError("database API returned incomplete or mismatched parent identity")
	}
	return db, nil
}

func databaseUserOutput(name, id, username string, db *godo.Database, user *godo.DatabaseUser, credentials bool) (*interfaces.ResourceOutput, error) {
	if err := validateDatabaseUser(user, username); err != nil {
		return nil, err
	}
	outputs := map[string]any{"database_id": id, "username": username, "role": user.Role}
	if db.Connection != nil {
		outputs["host"] = db.Connection.Host
		outputs["port"] = db.Connection.Port
		outputs["database"] = db.Connection.Database
	}
	if credentials {
		uri, err := databaseTLSURI(db, username, user.Password)
		if err != nil {
			return nil, err
		}
		outputs["uri"], outputs["password"] = uri, user.Password
	}
	return &interfaces.ResourceOutput{Name: name, Type: "digitalocean.database_user", ProviderID: id + "/" + url.PathEscape(username), Outputs: outputs, Sensitive: map[string]bool{"uri": true, "password": true}, Status: db.Status}, nil
}

func databaseTLSURI(db *godo.Database, username, password string) (string, error) {
	if db == nil || db.Connection == nil || password == "" {
		return "", databaseValidationError("database credential response is incomplete")
	}
	c := db.Connection
	if c.Host == "" || strings.ContainsAny(c.Host, "/?#@% \t\r\n") || (strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil) || c.Port < 1 || c.Port > 65535 || c.Database == "" || strings.ContainsFunc(c.Database, unicode.IsControl) {
		return "", databaseValidationError("database connection endpoint is invalid")
	}
	uri := url.URL{User: url.UserPassword(username, password), Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Path: "/" + c.Database, RawPath: "/" + url.PathEscape(c.Database)}
	query := url.Values{}
	switch db.EngineSlug {
	case "pg":
		uri.Scheme = "postgresql"
		query.Set("sslmode", "require")
	case "mysql":
		uri.Scheme = "mysql"
		query.Set("tls", "true")
	default:
		return "", databaseValidationError("database credential URI requires PostgreSQL or MySQL")
	}
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func databaseEpochFromConfig(config map[string]any, key string) (string, bool, error) {
	value, present := config[key]
	if !present {
		return "", false, nil
	}
	epoch, ok := value.(string)
	if !ok || strings.TrimSpace(epoch) == "" {
		return "", false, databaseValidationError(key + " must be a nonempty string")
	}
	return epoch, true, nil
}

func preserveDatabaseState(out *interfaces.ResourceOutput, prior *interfaces.ResourceState, epochKey string) {
	if prior == nil {
		return
	}
	if _, present := out.Outputs[epochKey]; !present {
		if epoch, ok := prior.Outputs[epochKey].(string); ok && strings.TrimSpace(epoch) != "" {
			out.Outputs[epochKey] = epoch
		}
	}
	for key, sensitiveOutput := range out.Sensitive {
		if value := prior.Outputs[key]; sensitiveOutput && sensitive.IsPlaceholder(value) {
			out.Outputs[key] = value
			// References are already routed; marking them sensitive would replace
			// stored credentials with the reference text on a no-reset update.
			delete(out.Sensitive, key)
		}
	}
}

type databaseOperationFailure struct {
	operation      string
	cause          error
	classification error
}

func (e *databaseOperationFailure) Error() string {
	if e.classification != nil {
		return e.operation + ": " + e.classification.Error()
	}
	return e.operation + " failed"
}

func (e *databaseOperationFailure) Unwrap() []error {
	if e.classification != nil {
		return []error{e.classification, e.cause}
	}
	return []error{e.cause}
}

func databaseAPIError(operation string, cause error) error {
	var response *godo.ErrorResponse
	var classification error
	if errors.As(cause, &response) && response.Response != nil {
		classification = sentinelForStatus(response.Response.StatusCode)
	}
	return &databaseOperationFailure{operation: operation, cause: cause, classification: classification}
}

func databaseValidationError(message string) error {
	return fmt.Errorf("%s: %w", message, interfaces.ErrValidation)
}
