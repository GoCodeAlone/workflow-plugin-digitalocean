package internal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/secrets"
)

const releasedHostURL = "https://github.com/GoCodeAlone/workflow/releases/download/v0.86.1/wfctl-linux-amd64"
const releasedHostSize int64 = 153448928
const releasedHostSHA256 = "b2522ff83ea13719faa1f7d290505875693b807f048521bcd9661b7941b61503"

func releasedHostMode(getenv func(string) string, goos, arch string) (required, enabled bool, err error) {
	ci := strings.ToLower(getenv("CI"))
	required = (ci != "" && ci != "false" && ci != "0") || getenv("GITHUB_ACTIONS") == "true"
	if !required {
		return false, getenv("WORKFLOW_DO_TASK7_WFCTL") != "", nil
	}
	// These getenv calls are Go-cache freshness inputs, never authority.
	_ = getenv("GITHUB_RUN_ID")
	_ = getenv("GITHUB_RUN_ATTEMPT")
	for _, key := range []string{"WORKFLOW_DO_TASK7_WFCTL", "WORKFLOW_DO_TASK7_BUILD_ROOT", "WORKFLOW_DO_TASK7_EVIDENCE"} {
		if getenv(key) != "" {
			return true, true, errors.New("required released host rejects local overrides")
		}
	}
	if goos != "linux" || arch != "amd64" {
		return true, true, errors.New("required released host needs linux/amd64")
	}
	return true, true, nil
}

func downloadReleasedHost(ctx context.Context, dir string, transport http.RoundTripper, size int64, digest string) (string, error) {
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !releasedHostOrigin(req.URL) {
			return errors.New("released host redirect denied")
		}
		req.Header = make(http.Header)
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasedHostURL, nil)
	if err != nil {
		return "", errors.New("released host request failed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("released host download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("released host HTTP status rejected")
	}
	file, err := os.CreateTemp(dir, "wfctl-")
	if err != nil {
		return "", errors.New("released host temporary file failed")
	}
	verified := false
	defer func() {
		_ = file.Close()
		if !verified {
			_ = os.Remove(file.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, size+1))
	if err != nil || ctx.Err() != nil || n != size || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return "", errors.New("released host integrity check failed")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("released host file close failed")
	}
	if err := os.Chmod(file.Name(), 0o700); err != nil {
		return "", errors.New("released host executable permission failed")
	}
	verified = true
	return file.Name(), nil
}

func releasedHostOrigin(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Opaque == "" && u.Fragment == "" && (u.Host == "github.com" || u.Host == "release-assets.githubusercontent.com")
}

func releasedHostTransport() *http.Transport {
	return &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
}

func releasedHostEnvironment(root string) []string {
	return []string{"HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "TMPDIR=" + filepath.Join(root, "tmp"), "PATH=/usr/bin:/bin", "LANG=C", "GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "WFCTL_NO_UPDATE_CHECK=1"}
}

func releasedHostGoTool() (string, error) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("Go build tool unavailable")
	}
	return goTool, nil
}

func releasedHostCloseAPI(api *httptest.Server, cancel context.CancelFunc) {
	cancel()
	api.CloseClientConnections()
	api.Close()
}

func TestReleasedHostFixtureCancellationClosesActiveHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	api.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	api.Start()
	defer api.Close()
	transport := releasedHostTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Post(api.URL, "application/json", strings.NewReader(`{"fixture":true}`))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owned fixture request did not start")
	}
	closed := make(chan struct{})
	go func() { releasedHostCloseAPI(api, cancel); close(closed) }()
	select {
	case <-closed:
	case <-time.After(300 * time.Millisecond):
		cancel()
		<-closed
		t.Error("fixture cleanup did not cancel an active unread request")
	}
	<-requestDone
	if probe, err := net.DialTimeout("tcp", strings.TrimPrefix(api.URL, "http://"), time.Second); err == nil {
		_ = probe.Close()
		t.Fatal("owned listener survived cancellation cleanup")
	}
}

func releasedHostCommand(ctx context.Context, env []string, dir, executable string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return errors.New("owned process group cleanup failed")
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	return cmd
}

const releasedHostVersionOutputLimit = 4096
const releasedHostVersionEvidenceLimit = 16384

type releasedHostVersionStatus string

const (
	releasedHostVersionCompleted    releasedHostVersionStatus = "completed"
	releasedHostVersionNonzero      releasedHostVersionStatus = "nonzero"
	releasedHostVersionTimeout      releasedHostVersionStatus = "timeout"
	releasedHostVersionCanceled     releasedHostVersionStatus = "canceled"
	releasedHostVersionOverflow     releasedHostVersionStatus = "overflow"
	releasedHostVersionStartFailure releasedHostVersionStatus = "start-failure"
	releasedHostVersionWaitFailure  releasedHostVersionStatus = "wait-failure"
)

type releasedHostVersionResult struct {
	Stdout, Stderr, Combined []byte
	Status                   releasedHostVersionStatus
	ExitCode                 int
	Overflow                 bool
}

func captureReleasedHostVersion(ctx context.Context, env []string, dir, executable string, args ...string) releasedHostVersionResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	capture := releasedHostVersionCapture{cancel: cancel, result: releasedHostVersionResult{
		Stdout: make([]byte, 0, releasedHostVersionOutputLimit), Stderr: make([]byte, 0, releasedHostVersionOutputLimit), Combined: make([]byte, 0, releasedHostVersionOutputLimit),
		Status: releasedHostVersionCompleted, ExitCode: -1,
	}}
	cmd := releasedHostCommand(ctx, env, dir, executable, args...)
	cmd.Stdout = releasedHostVersionWriter{capture: &capture, stdout: true}
	cmd.Stderr = releasedHostVersionWriter{capture: &capture}
	err := cmd.Run()
	// Also kill descendants if the leader exited before cancellation/WaitDelay.
	cleanupErr := cmd.Cancel()
	result := capture.result
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case result.Overflow:
		result.Status = releasedHostVersionOverflow
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Status = releasedHostVersionTimeout
	case ctx.Err() != nil:
		result.Status = releasedHostVersionCanceled
	case cmd.ProcessState == nil:
		result.Status = releasedHostVersionStartFailure
	case cleanupErr != nil || errors.Is(err, exec.ErrWaitDelay):
		result.Status = releasedHostVersionWaitFailure
	case err != nil:
		result.Status = releasedHostVersionNonzero
	}
	return result
}

type releasedHostVersionCapture struct {
	mu     sync.Mutex
	result releasedHostVersionResult
	cancel context.CancelFunc
}

type releasedHostVersionWriter struct {
	capture *releasedHostVersionCapture
	stdout  bool
}

func (w releasedHostVersionWriter) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(len(p), releasedHostVersionOutputLimit-len(c.result.Combined))
	if w.stdout {
		c.result.Stdout = append(c.result.Stdout, p[:n]...)
	} else {
		c.result.Stderr = append(c.result.Stderr, p[:n]...)
	}
	// Preserve raw read-arrival order, not a synthetic stdout/stderr concatenation.
	c.result.Combined = append(c.result.Combined, p[:n]...)
	if n < len(p) {
		c.result.Overflow = true
		c.cancel()
	}
	return len(p), nil
}

func (r releasedHostVersionResult) diagnostic(redactor *secrets.Redactor) string {
	summary := fmt.Sprintf("released wfctl version: status=%s exit=%d overflow=%t stdout_bytes=%d stderr_bytes=%d combined_bytes=%d",
		r.Status, r.ExitCode, r.Overflow, len(r.Stdout), len(r.Stderr), len(r.Combined))
	// Never redact a clipped prefix: it may end partway through a known value.
	if redactor == nil || r.Overflow || r.Status == releasedHostVersionTimeout || r.Status == releasedHostVersionCanceled || r.Status == releasedHostVersionWaitFailure {
		return summary + " streams=omitted (incomplete capture or redactor unavailable)"
	}
	streams := fmt.Sprintf(" stdout=%q stderr=%q combined=%q", redactor.Redact(string(r.Stdout)), redactor.Redact(string(r.Stderr)), redactor.Redact(string(r.Combined)))
	if len(summary)+len(streams) > releasedHostVersionEvidenceLimit {
		return summary + " streams=omitted (quoted evidence limit)"
	}
	return summary + streams
}

func TestReleasedHostVersionCapture(t *testing.T) {
	for _, tc := range []struct {
		name, script, stdout, stderr string
		status                       releasedHostVersionStatus
		exitCode                     int
		admitted                     bool
	}{
		{"stderr version", `printf 'v0.86.1\n' >&2`, "", "v0.86.1\n", releasedHostVersionCompleted, 0, true},
		{"stdout version", `printf 'v0.86.1\n'`, "v0.86.1\n", "", releasedHostVersionCompleted, 0, true},
		{"extra stderr", `printf 'v0.86.1\n'; printf 'unexpected\n' >&2`, "v0.86.1\n", "unexpected\n", releasedHostVersionCompleted, 0, false},
		{"extra stdout", `printf 'unexpected\n'; printf 'v0.86.1\n' >&2`, "unexpected\n", "v0.86.1\n", releasedHostVersionCompleted, 0, false},
		{"wrong version", `printf 'v0.86.0\n' >&2`, "", "v0.86.0\n", releasedHostVersionCompleted, 0, false},
		{"malformed", `printf 'v0.86.1\000\033\377\n' >&2`, "", "v0.86.1\x00\x1b\xff\n", releasedHostVersionCompleted, 0, false},
		{"empty", `:`, "", "", releasedHostVersionCompleted, 0, false},
		{"nonzero", `printf 'v0.86.1\n'; printf 'denied\n' >&2; exit 7`, "v0.86.1\n", "denied\n", releasedHostVersionNonzero, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			result := captureReleasedHostVersion(t.Context(), releasedHostEnvironment(root), root, "/bin/sh", "-c", tc.script)
			if string(result.Stdout) != tc.stdout || string(result.Stderr) != tc.stderr {
				t.Fatal("version evidence lost separate raw streams")
			}
			if result.Status != tc.status || result.ExitCode != tc.exitCode || result.Overflow {
				t.Fatalf("version execution status = %s/%d/%t; want %s/%d/false", result.Status, result.ExitCode, result.Overflow, tc.status, tc.exitCode)
			}
			// Cross-pipe read order is not guaranteed; never synthesize stdout-only admission.
			combined := string(result.Combined)
			if combined != tc.stdout+tc.stderr && combined != tc.stderr+tc.stdout {
				t.Fatal("combined version evidence changed raw command bytes")
			}
			if admitted := result.Status == releasedHostVersionCompleted && strings.TrimSpace(combined) == "v0.86.1"; admitted != tc.admitted {
				t.Fatal("strict raw combined version admission changed")
			}
			evidence := result.diagnostic(secrets.NewRedactor())
			if !strings.Contains(evidence, "stdout="+strconv.Quote(tc.stdout)) || !strings.Contains(evidence, "stderr="+strconv.Quote(tc.stderr)) || !strings.Contains(evidence, "combined="+strconv.Quote(combined)) {
				t.Fatal("version diagnostic did not safely quote actual streams")
			}
			if strings.ContainsAny(evidence, "\x00\x1b\n\r") {
				t.Fatal("version diagnostic permits control-byte injection")
			}
		})
	}
}

func TestReleasedHostVersionDiagnosticPrivacy(t *testing.T) {
	root := t.TempDir()
	const sentinel = "task27-private-fixture-value"
	result := captureReleasedHostVersion(t.Context(), releasedHostEnvironment(root), root, "/bin/sh", "-c",
		`printf '%s\n' "$1"; printf '%s\n' "$2" >&2; exit 9`, "probe", sentinel, root)
	redactor := secrets.NewRedactor()
	redactor.AddValue("fixture", sentinel)
	redactor.AddValue("owned-root", root)
	evidence := result.diagnostic(redactor)
	if strings.Contains(evidence, sentinel) || strings.Contains(evidence, root) || !strings.Contains(evidence, "[REDACTED:fixture]") || !strings.Contains(evidence, "[REDACTED:owned-root]") {
		t.Fatal("version diagnostic exposed or discarded registered private evidence")
	}
	if string(result.Stdout) != sentinel+"\n" || string(result.Stderr) != root+"\n" || !strings.Contains(string(result.Combined), sentinel) {
		t.Fatal("diagnostic redaction modified raw consumer/admission bytes")
	}
	if result.Status != releasedHostVersionNonzero || result.ExitCode != 9 || !strings.Contains(evidence, "status=nonzero exit=9") {
		t.Fatal("private diagnostic lost nonzero status")
	}
	if unarmed := result.diagnostic(nil); strings.Contains(unarmed, sentinel) || strings.Contains(unarmed, root) {
		t.Fatal("unarmed diagnostic emitted raw private bytes")
	}
}

func TestReleasedHostVersionCaptureStatus(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := captureReleasedHostVersion(ctx, releasedHostEnvironment(root), root, "/bin/sleep", "5")
	if result.Status != releasedHostVersionTimeout || result.ExitCode != -1 || time.Since(start) > 2*time.Second || !strings.Contains(result.diagnostic(secrets.NewRedactor()), "status=timeout") {
		t.Fatal("version timeout was not bounded and classified")
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	result = captureReleasedHostVersion(canceled, releasedHostEnvironment(root), root, "/bin/sleep", "5")
	if result.Status != releasedHostVersionCanceled || result.ExitCode != -1 {
		t.Fatal("version cancellation was not classified")
	}
	result = captureReleasedHostVersion(t.Context(), releasedHostEnvironment(root), root, filepath.Join(root, "private-missing-command"))
	if result.Status != releasedHostVersionStartFailure || result.ExitCode != -1 || strings.Contains(result.diagnostic(secrets.NewRedactor()), root) {
		t.Fatal("version start failure leaked command path or lacked status")
	}
}

func TestReleasedHostVersionCaptureOverflow(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr", "combined"} {
		t.Run(stream, func(t *testing.T) {
			root := t.TempDir()
			const sentinel = "task27-boundary-private-value"
			redactor := secrets.NewRedactor()
			redactor.AddValue("fixture", sentinel)
			prefix := strings.Repeat("x", releasedHostVersionOutputLimit-len(sentinel)/2)
			payload := prefix + sentinel
			script := `printf '%s' "$1"; exec /bin/sleep 5`
			switch stream {
			case "stderr":
				script = `exec 1>&2; ` + script
			case "combined":
				payload = strings.Repeat("x", releasedHostVersionOutputLimit/2-1)
				script = `printf '%s' "$1"; printf '%s' "$1" >&2; printf '%s' "$2"; exec /bin/sleep 5`
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			result := captureReleasedHostVersion(ctx, releasedHostEnvironment(root), root, "/bin/sh", "-c", script, "probe", payload, sentinel)
			if result.Status != releasedHostVersionOverflow || !result.Overflow || time.Since(start) > time.Second {
				t.Fatal("version output overflow did not cancel promptly")
			}
			for _, raw := range [][]byte{result.Stdout, result.Stderr, result.Combined} {
				if len(raw) > releasedHostVersionOutputLimit {
					t.Fatal("version capture allocated unbounded output")
				}
			}
			evidence := result.diagnostic(redactor)
			if strings.Contains(evidence, sentinel[:len(sentinel)/2]) || strings.Contains(evidence, "xxx") || !strings.Contains(evidence, "omitted") || !strings.Contains(evidence, "overflow=true") || len(evidence) > releasedHostVersionEvidenceLimit {
				t.Fatal("overflow diagnostic leaked partial clipped evidence or lost classification")
			}
		})
	}
}

func verifyReleasedHostFile(path string, size int64, digest string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("local released host cannot be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("local released host size rejected")
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, size+1)); err != nil || n != size || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return errors.New("local released host digest rejected")
	}
	return nil
}

func reapReleasedHostChildren(path string, kill bool) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && kill {
		return nil
	}
	if err != nil {
		return errors.New("owned child ledger unavailable")
	}
	deadline := time.Now().Add(2 * time.Second)
	for _, raw := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(raw)
		if err != nil || pid < 2 || pid == os.Getpid() {
			return errors.New("owned child ledger invalid")
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			continue
		}
		if !kill {
			return errors.New("owned native child still running")
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return errors.New("owned native child termination failed")
		}
		for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			if time.Now().After(deadline) {
				return errors.New("owned native child not reaped")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil
}

type releasedHostFailingBody struct {
	ctx     context.Context
	first   bool
	timeout bool
	inspect func()
}

func (b *releasedHostFailingBody) Read(p []byte) (int, error) {
	if !b.first {
		b.first = true
		p[0] = 'x'
		return 1, nil
	}
	b.inspect()
	if b.timeout {
		<-b.ctx.Done()
	}
	return 0, errors.New("sensitive-body-error")
}

func (*releasedHostFailingBody) Close() error { return nil }

func TestReleasedHostPartialDownloadNeverExecutable(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			inspected := false
			body := &releasedHostFailingBody{ctx: ctx, timeout: timeout, inspect: func() {
				files, err := os.ReadDir(dir)
				if err != nil || len(files) != 1 {
					t.Fatal("partial download ownership missing")
				}
				info, err := files[0].Info()
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("unverified partial bytes became executable")
				}
				inspected = true
			}}
			transport := releasedHostRoundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r}, nil
			})
			file, err := downloadReleasedHost(ctx, dir, transport, 2, strings.Repeat("0", 64))
			files, _ := os.ReadDir(dir)
			if err == nil || file != "" || len(files) != 0 || !inspected || strings.Contains(err.Error(), "sensitive") {
				t.Fatal("failed partial download was not safely cleaned")
			}
		})
	}
}

func TestReleasedHostRedirectLimit(t *testing.T) {
	calls := 0
	transport := releasedHostRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {releasedHostURL}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	dir := t.TempDir()
	file, err := downloadReleasedHost(t.Context(), dir, transport, 1, strings.Repeat("0", 64))
	files, _ := os.ReadDir(dir)
	if err == nil || file != "" || len(files) != 0 || calls != 4 {
		t.Fatal("download redirects are not bounded")
	}
}

func TestReleasedHostFailureCleanupReapsOwnedChild(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cmd := releasedHostCommand(ctx, releasedHostEnvironment(root), "", "/bin/sleep", "0.5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	path := filepath.Join(root, "owned-pids.txt")
	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reapReleasedHostChildren(path, false); err == nil {
		t.Fatal("consumer would report PASS with an owned child still running")
	}
	if err := reapReleasedHostChildren(path, true); err != nil {
		t.Fatal(err)
	}
	if err := <-waited; err == nil {
		t.Fatal("cleanup did not kill owned child")
	}
	if cmd.Process.Signal(syscall.Signal(0)) == nil {
		t.Fatal("failed consumer left an owned process")
	}
}

func TestReleasedHostTransportHasNoAmbientCredentials(t *testing.T) {
	transport := releasedHostTransport()
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("download transport inherits proxy credentials or waives TLS")
	}
}

func TestReleasedHostChildEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "credential-sentinel")
	t.Setenv("DIGITALOCEAN_TOKEN", "credential-sentinel")
	t.Setenv("HTTPS_PROXY", "credential-sentinel")
	t.Setenv("CI", "true")
	root := t.TempDir()
	for _, entry := range releasedHostEnvironment(root) {
		key, value, _ := strings.Cut(entry, "=")
		if value == "credential-sentinel" || key == "CI" || key == "GITHUB_ACTIONS" {
			t.Fatal("native child inherited CI/cloud credentials")
		}
		if (key == "HOME" || key == "TMPDIR" || key == "XDG_CONFIG_HOME") && !strings.HasPrefix(value, root+string(filepath.Separator)) {
			t.Fatal("native child paths are not owned")
		}
	}
}

func TestReleasedHostEnvironmentDisablesBackgroundUpdateCheck(t *testing.T) {
	t.Setenv("WFCTL_NO_UPDATE_CHECK", "")
	for _, entry := range releasedHostEnvironment(t.TempDir()) {
		if entry == "WFCTL_NO_UPDATE_CHECK=1" {
			return
		}
	}
	t.Fatal("released host environment permits background update lookup")
}

func TestReleasedHostRuntimeEnvironmentPreventsToolchainSelection(t *testing.T) {
	const child = "released-host-runtime-environment-child"
	if os.Args[len(os.Args)-1] == child {
		if os.Getenv("GOTOOLCHAIN") != "local" {
			t.Fatal("runtime environment permits automatic toolchain selection")
		}
		if os.Getenv("PATH") != "/usr/bin:/bin" || os.Getenv("GOMODCACHE") != "" || os.Getenv("GOPATH") != "" {
			t.Fatal("runtime environment changed its fixed PATH or redirects compiler caches")
		}
		home := os.Getenv("HOME")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal("cannot create owned runtime home")
		}
		if err := os.WriteFile(filepath.Join(home, "runtime-child"), nil, 0o600); err != nil {
			t.Fatal("cannot write owned runtime marker")
		}
		return
	}
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot resolve compiled runtime probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := releasedHostCommand(ctx, releasedHostEnvironment(root), root, executable,
		"-test.run=^TestReleasedHostRuntimeEnvironmentPreventsToolchainSelection$", "--", child)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiled runtime environment probe failed: %s", output)
	}
	if cmd.Process == nil || cmd.Process.Signal(syscall.Signal(0)) == nil {
		t.Fatal("runtime environment probe left an owned child")
	}
	if _, err := os.Stat(filepath.Join(root, "home", "runtime-child")); err != nil {
		t.Fatal("compiled runtime environment probe did not execute")
	}
	if _, err := os.Stat(filepath.Join(root, "home", "go", "pkg", "mod")); !os.IsNotExist(err) {
		t.Fatal("runtime environment probe produced a compiler cache")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal("ordinary owned runtime cleanup failed")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("owned runtime root survived ordinary cleanup")
	}
}

func TestReleasedHostCommandTimeoutReapsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	cmd := releasedHostCommand(ctx, releasedHostEnvironment(t.TempDir()), "", "/bin/sleep", "0.3")
	start := time.Now()
	if err := cmd.Run(); err == nil || time.Since(start) > 250*time.Millisecond {
		t.Fatal("native command ignored its context deadline")
	}
	if cmd.Process == nil || cmd.Process.Signal(syscall.Signal(0)) == nil {
		t.Fatal("owned command was not reaped")
	}
}

type releasedHostRoundTrip func(*http.Request) (*http.Response, error)

func (f releasedHostRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReleasedHostMode(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		env                       map[string]string
		goos, arch                string
		required, enabled, denied bool
	}{
		{name: "ordinary local", goos: "darwin", arch: "arm64"},
		{name: "deliberate local", env: map[string]string{"WORKFLOW_DO_TASK7_WFCTL": "/local/verified-host"}, goos: "darwin", arch: "arm64", enabled: true},
		{name: "CI cannot skip", env: map[string]string{"CI": "true"}, goos: "linux", arch: "amd64", required: true, enabled: true},
		{name: "Actions cannot disable CI", env: map[string]string{"CI": "false", "GITHUB_ACTIONS": "true"}, goos: "linux", arch: "amd64", required: true, enabled: true},
		{name: "unsupported OS fails", env: map[string]string{"CI": "true"}, goos: "darwin", arch: "arm64", required: true, enabled: true, denied: true},
		{name: "unsupported arch fails", env: map[string]string{"CI": "true"}, goos: "linux", arch: "arm64", required: true, enabled: true, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			required, enabled, err := releasedHostMode(func(k string) string { return tc.env[k] }, tc.goos, tc.arch)
			if required != tc.required || enabled != tc.enabled || (err != nil) != tc.denied {
				t.Fatalf("required=%t enabled=%t denied=%t; want %t/%t/%t", required, enabled, err != nil, tc.required, tc.enabled, tc.denied)
			}
		})
	}
	for _, override := range []string{"WORKFLOW_DO_TASK7_WFCTL", "WORKFLOW_DO_TASK7_BUILD_ROOT", "WORKFLOW_DO_TASK7_EVIDENCE"} {
		t.Run(override, func(t *testing.T) {
			_, _, err := releasedHostMode(func(k string) string {
				if k == "CI" {
					return "true"
				}
				if k == override {
					return "sensitive-override"
				}
				return ""
			}, "linux", "amd64")
			if err == nil || strings.Contains(err.Error(), "sensitive-override") {
				t.Fatal("required CI accepted or exposed an arbitrary override")
			}
		})
	}
}

func TestReleasedHostCacheInputs(t *testing.T) {
	reads := map[string]bool{}
	_, _, _ = releasedHostMode(func(k string) string {
		reads[k] = true
		if k == "CI" {
			return "true"
		}
		return ""
	}, "linux", "amd64")
	if !reads["GITHUB_RUN_ID"] || !reads["GITHUB_RUN_ATTEMPT"] {
		t.Fatal("required proof did not consume both Go-cache metadata inputs")
	}
	// Exercise real getenv instrumentation too; the command-level cache
	// experiment uses this unit boundary as its cacheable control, not a
	// replacement for the uncached native consumer proof.
	_, _, _ = releasedHostMode(os.Getenv, runtime.GOOS, runtime.GOARCH)
}

func TestReleasedHostDownloadIntegrityAndCleanup(t *testing.T) {
	const payload = "verified-test-asset"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	for _, tc := range []struct {
		name, body string
		status     int
		size       int64
		digest     string
		ok         bool
	}{
		{"verified", payload, http.StatusOK, int64(len(payload)), digest, true},
		{"HTTP failure", "sensitive-error-body", http.StatusForbidden, int64(len(payload)), digest, false},
		{"short", payload[:3], http.StatusOK, int64(len(payload)), digest, false},
		{"oversize", payload + "x", http.StatusOK, int64(len(payload)), digest, false},
		{"digest mismatch", payload, http.StatusOK, int64(len(payload)), strings.Repeat("0", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			transport := releasedHostRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != releasedHostURL || len(r.Header) != 0 {
					t.Fatal("download changed pinned URL or supplied headers")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: r}, nil
			})
			file, err := downloadReleasedHost(t.Context(), dir, transport, tc.size, tc.digest)
			if (err == nil) != tc.ok || calls != 1 {
				t.Fatalf("verified=%t requests=%d; want %t/1", err == nil, calls, tc.ok)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tc.ok {
				if file != "" || len(entries) != 0 || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "https:") {
					t.Fatal("failure exposed download data or left partial executable")
				}
				return
			}
			data, err := os.ReadFile(file)
			if err != nil || string(data) != payload || len(entries) != 1 || filepath.Dir(file) != dir {
				t.Fatal("verified host bytes or ownership differ")
			}
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("only verified bytes may become executable")
			}
		})
	}
}

func TestReleasedHostDownloadRedirects(t *testing.T) {
	const payload = "verified-test-asset"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	for _, target := range []string{
		"https://release-assets.githubusercontent.com/asset?signed=sensitive",
		"http://release-assets.githubusercontent.com/asset",
		"https://user:password@release-assets.githubusercontent.com/asset",
		"https://release-assets.githubusercontent.com:444/asset",
		"https://unreviewed.example/asset",
		"https://release-assets.githubusercontent.com.evil.example/asset",
	} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			transport := releasedHostRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {target}, "Set-Cookie": {"secret=cookie"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("redirect inherited credentials")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
			})
			file, err := downloadReleasedHost(t.Context(), dir, transport, int64(len(payload)), digest)
			approved := target == "https://release-assets.githubusercontent.com/asset?signed=sensitive"
			if (err == nil) != approved {
				t.Fatalf("approved=%t success=%t", approved, err == nil)
			}
			if !approved && (calls != 1 || file != "") {
				t.Fatal("denied redirect reached another origin or left a host")
			}
			if err != nil && (strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "https:") || strings.Contains(err.Error(), "password")) {
				t.Fatal("raw redirect error leaked")
			}
		})
	}
}

func TestReleasedHostDownloadTimeoutAndRawError(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
			defer cancel()
			dir := t.TempDir()
			transport := releasedHostRoundTrip(func(r *http.Request) (*http.Response, error) {
				if timeout {
					<-r.Context().Done()
				}
				return nil, errors.New("https://user:credential@unreviewed.example?signed=secret")
			})
			file, err := downloadReleasedHost(ctx, dir, transport, 1, strings.Repeat("0", 64))
			entries, _ := os.ReadDir(dir)
			if err == nil || file != "" || len(entries) != 0 || strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "https:") {
				t.Fatal("timeout/transport failure was not bounded, sanitized and cleaned")
			}
		})
	}
}
