package digitalocean_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Supplier release asset623675847, release407438170. The public release
// metadata was independently retrieved before source preparation. Neither an
// environment variable nor candidate manifest selects this expected digest.
const bootstrapSDKURL = "https://github.com/actions/go-versions/releases/download/1.27.2-37875086547/go-1.27.2-linux-x64.tar.gz"
const bootstrapSDKArchiveSHA = "d5ea5f4ae405daa1d37ab1d54706f25aee826f8ccc9ce06e7d89aeb4ea308051"
const bootstrapSDKArchiveBytes = 71657186

type bootstrapSDKFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func bootstrapSDKDownloadBudget(now, deadline time.Time) (time.Duration, error) {
	budget := 2 * time.Minute
	if !deadline.IsZero() {
		remaining := deadline.Sub(now) - 15*time.Second
		if remaining <= 0 {
			return 0, errors.New("supplier download has no test-deadline publication reserve")
		}
		if remaining < budget {
			budget = remaining
		}
	}
	return budget, nil
}

func bootstrapSDKArchive(t *testing.T) []byte {
	t.Helper()
	root := os.Getenv("RUNNER_TEMP")
	physical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) || physical != root {
		t.Fatal("SDK verification requires canonical existing runner temporary storage")
	}
	path := filepath.Join(root, "provider-bootstrap-go1272-supplier.tar.gz")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		// Root CI already runs the pinned setup-go compiler. This additional
		// credential-free acquisition authenticates SDK bytes before any Go
		// child. The preparation helper also performs an independent non-Go
		// installed-SDK comparison BEFORE compiling/running the root tests.
		deadline, _ := t.Deadline()
		budget, err := bootstrapSDKDownloadBudget(time.Now(), deadline)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), budget)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, bootstrapSDKURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: budget, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 || req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Host != "github.com" && req.URL.Host != "release-assets.githubusercontent.com") {
				return errors.New("supplier redirect outside the admitted HTTPS release transport")
			}
			return nil
		}}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("bounded credential-free supplier SDK download failed")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || (response.ContentLength != -1 && response.ContentLength != bootstrapSDKArchiveBytes) {
			t.Fatal("supplier SDK status or declared size mismatch")
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, bootstrapSDKArchiveBytes+1))
		if err != nil || len(data) != bootstrapSDKArchiveBytes || bootstrapHash(data) != bootstrapSDKArchiveSHA {
			t.Fatal("supplier SDK is not the independently approved exact archive")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal("supplier verification data needs exclusive owned creation")
		}
		n, writeErr := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil || n != len(data) {
			t.Fatal("supplier verification data persistence failed")
		}
	} else if err != nil {
		t.Fatal("supplier verification input is inaccessible")
	}
	data, err := bootstrapRegularFile(path, bootstrapSDKArchiveBytes)
	if err != nil || len(data) != bootstrapSDKArchiveBytes || bootstrapHash(data) != bootstrapSDKArchiveSHA {
		t.Fatal("existing supplier SDK data fails approved size/hash authentication")
	}
	return data
}

func bootstrapSupplierSDK(data []byte) ([]bootstrapSDKFile, error) {
	// Authenticate compressed bytes before permitting the inert parser to see
	// them. Never extract files or execute the archive's setup.sh installer.
	if len(data) != bootstrapSDKArchiveBytes || bootstrapHash(data) != bootstrapSDKArchiveSHA {
		return nil, errors.New("SDK archive authentication failed before parsing")
	}
	return bootstrapSDKTarInventory(data)
}

func bootstrapSDKTarInventory(data []byte) ([]bootstrapSDKFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: 1024*1024*1024 + 1}
	reader := tar.NewReader(limited)
	var inventory []bootstrapSDKFile
	seen := map[string]bool{}
	for count := 0; ; count++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count >= 50000 {
			return nil, errors.New("supplier SDK tar entry limit or parse failure")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir && (header.Name == "." || header.Name == "./") {
			if seen["."] || header.Size != 0 || header.Linkname != "" {
				return nil, errors.New("duplicate or invalid literal supplier SDK root directory")
			}
			seen["."] = true
			continue
		}
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !safeBootstrapPath(name) || seen[name] || header.Linkname != "" || header.Size < 0 || header.Size > 64*1024*1024 || (header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
			return nil, errors.New("unsafe, duplicate, linked or special supplier SDK entry")
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			if header.Size != 0 {
				return nil, errors.New("nonempty supplier SDK directory")
			}
			continue
		}
		hash := sha256.New()
		n, err := io.Copy(hash, reader)
		if err != nil || n != header.Size {
			return nil, errors.New("truncated supplier SDK component")
		}
		// The supplier copies this flat archive to the toolcache then removes
		// setup.sh. It is authenticated data here, never an invoked installer.
		if name != "setup.sh" {
			inventory = append(inventory, bootstrapSDKFile{Path: name, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Bytes: n})
		}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 {
		return nil, errors.New("supplier SDK decompression bound or checksum failure")
	}
	if len(inventory) == 0 || !seen["bin/go"] || !seen["VERSION"] || !seen["src/runtime/runtime2.go"] || !seen["pkg/tool/linux_amd64/compile"] {
		return nil, errors.New("supplier SDK does not contain the complete expected layout")
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Path < inventory[j].Path })
	return inventory, nil
}

func verifyBootstrapSDKFiles(root string, inventory []bootstrapSDKFile) error {
	if len(inventory) == 0 || len(inventory) > 50000 {
		return errors.New("installed SDK requires authenticated complete supplier inventory")
	}
	expected := map[string]bootstrapSDKFile{}
	for _, entry := range inventory {
		if !safeBootstrapPath(entry.Path) || entry.Bytes < 0 || entry.Bytes > 64*1024*1024 || expected[entry.Path].Path != "" {
			return errors.New("invalid SDK comparison inventory")
		}
		expected[entry.Path] = entry
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		entry, exists := expected[relative]
		if err != nil || !exists {
			return errors.New("unexpected installed SDK component")
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path {
			return errors.New("installed SDK component is linked or noncanonical")
		}
		data, err := bootstrapRegularFile(path, entry.Bytes)
		if err != nil || int64(len(data)) != entry.Bytes || bootstrapHash(data) != entry.SHA256 {
			return errors.New("installed SDK bytes differ from the approved supplier archive")
		}
		count++
		return nil
	})
	if err != nil || count != len(expected) {
		return errors.New("installed SDK complete supplier inventory comparison failed")
	}
	return nil
}

func TestBootstrapSDKRejectsChangedComponentsBeforeInvocation(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "was-executed")
	path := filepath.Join(root, "go")
	original := []byte("#!/bin/sh\nexit 0\n")
	if os.WriteFile(path, original, 0o700) != nil {
		t.Fatal("cannot materialize owned SDK component control")
	}
	inventory := []bootstrapSDKFile{{Path: "go", SHA256: bootstrapHash(original), Bytes: int64(len(original))}}
	if err := verifyBootstrapSDKFiles(root, inventory); err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(path, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700) != nil || verifyBootstrapSDKFiles(root, inventory) == nil {
		t.Fatal("changed bytes at an inventoried SDK path were admitted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hostile component executed before its byte comparison")
	}
	if os.WriteFile(path, original, 0o700) != nil || os.WriteFile(filepath.Join(root, "extra"), nil, 0o600) != nil || verifyBootstrapSDKFiles(root, inventory) == nil {
		t.Fatal("additional installed SDK source was admitted")
	}
	if _, err := bootstrapSupplierSDK([]byte("not the approved SDK")); err == nil {
		t.Fatal("unknown archive admitted before supplier authentication")
	}
}

func TestBootstrapSDKRejectsUnsafeArchiveEntries(t *testing.T) {
	makeArchive := func(extra *tar.Header) []byte {
		var data bytes.Buffer
		gz := gzip.NewWriter(&data)
		writer := tar.NewWriter(gz)
		if writer.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}) != nil {
			t.Fatal("cannot create positive literal root directory")
		}
		for _, name := range []string{"bin/go", "VERSION", "src/runtime/runtime2.go", "pkg/tool/linux_amd64/compile"} {
			if writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}) != nil {
				t.Fatal("cannot create positive SDK archive layout")
			}
		}
		if extra != nil && writer.WriteHeader(extra) != nil {
			t.Fatal("cannot create hostile inert archive entry")
		}
		if writer.Close() != nil || gz.Close() != nil {
			t.Fatal("cannot close owned inert archive control")
		}
		return data.Bytes()
	}
	if _, err := bootstrapSDKTarInventory(makeArchive(nil)); err != nil {
		t.Fatal("positive inert parser control did not pass")
	}
	for _, header := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg},
		{Name: "hostile", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
		{Name: "hostile", Typeflag: tar.TypeLink, Linkname: "elsewhere"},
		{Name: "hostile", Typeflag: tar.TypeFifo}, {Name: ".git/config", Typeflag: tar.TypeReg},
		{Name: "bin/go", Typeflag: tar.TypeReg},
		{Name: "./", Typeflag: tar.TypeDir}, {Name: "/", Typeflag: tar.TypeDir},
	} {
		if _, err := bootstrapSDKTarInventory(makeArchive(header)); err == nil {
			t.Fatal("unsafe SDK archive was admitted")
		}
	}
}

func TestBootstrapSDKDownloadReservesSuiteDeadline(t *testing.T) {
	now := time.Unix(0, 0)
	if budget, err := bootstrapSDKDownloadBudget(now, now.Add(time.Minute)); err != nil || budget != 45*time.Second {
		t.Fatal("supplier acquisition does not reserve deadline publication time")
	}
	if budget, err := bootstrapSDKDownloadBudget(now, now.Add(10*time.Minute)); err != nil || budget != 2*time.Minute {
		t.Fatal("supplier acquisition does not retain its finite transport cap")
	}
	if _, err := bootstrapSDKDownloadBudget(now, now.Add(15*time.Second)); err == nil {
		t.Fatal("supplier acquisition admits a spent suite deadline")
	}
}
