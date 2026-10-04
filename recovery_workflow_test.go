package digitalocean_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Entire CI, trust and executable inventory at immutable 5e5887a5.
const recoveryTrustedInventorySHA256 = "0d7179d840de07d0791ada93c2e1be1cc228a2eb37efbd93baba439ea669f250"

func recoveryTrustedInventory(root string) (string, int, error) {
	var paths []string
	for _, dir := range []string{".github", "scripts"} {
		if err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("CI inventory contains a symlink")
			}
			if !entry.IsDir() {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(relative))
			}
			return nil
		}); err != nil {
			return "", 0, err
		}
	}
	sort.Strings(paths)
	var inventory strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return "", 0, err
		}
		fmt.Fprintf(&inventory, "%s\x00%x\n", path, sha256.Sum256(data))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(inventory.String()))), len(paths), nil
}

func TestRecoveryCIAndTrustBytesUnchanged(t *testing.T) {
	digest, count, err := recoveryTrustedInventory(".")
	if err != nil {
		t.Fatal(err)
	}
	if count != 46 || digest != recoveryTrustedInventorySHA256 {
		t.Fatalf("CI/trust/script bytes differ from immutable base: files=%d digest=%s", count, digest)
	}
}

func TestRecoveryTrustedInventoryDetectsByteAndPresenceChanges(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".github", "scripts"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, ".github", "workflow.yml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, count, err := recoveryTrustedInventory(root)
	if err != nil || count != 1 {
		t.Fatal("cannot inspect fixture inventory")
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _, err := recoveryTrustedInventory(root)
	if err != nil || after == before {
		t.Fatal("byte change escaped regression")
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "new.sh"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	after, count, err = recoveryTrustedInventory(root)
	if err != nil || count != 2 || after == before {
		t.Fatal("new executable escaped regression")
	}
}
