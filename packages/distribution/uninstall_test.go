package distribution

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallRemovesVerifiedRuntimeAndOwnedExecutable(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "install")
	bin := filepath.Join(base, "bin")
	installed, err := Install(InstallOptions{
		BundleRoot: fixtureBundle(t, "0.7.0", "runtime-v1"), InstallRoot: root, BinDirectory: bin,
		CurrentOS: "testos", CurrentArch: "testarch",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Uninstall(UninstallOptions{InstallRoot: root, BinDirectory: bin})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RuntimeRemoved || !result.ExecutableRemoved || result.AlreadyAbsent || result.Executable != installed.Executable {
		t.Fatalf("unexpected uninstall result %#v", result)
	}
	for _, path := range []string{root, installed.Executable} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uninstall target remains %q: %v", path, err)
		}
	}
}

func TestUninstallSupportsPackageManagedCommandAndIsIdempotent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "install")
	if _, err := Install(InstallOptions{
		BundleRoot: fixtureBundle(t, "0.7.0", "runtime-v1"), InstallRoot: root, SkipBinLink: true,
		CurrentOS: "testos", CurrentArch: "testarch",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Uninstall(UninstallOptions{InstallRoot: root})
	if err != nil || !result.RuntimeRemoved || result.Executable != "" {
		t.Fatalf("unexpected package-managed uninstall %#v %v", result, err)
	}
	result, err = Uninstall(UninstallOptions{InstallRoot: root})
	if err != nil || !result.AlreadyAbsent || result.RuntimeRemoved {
		t.Fatalf("unexpected idempotent uninstall %#v %v", result, err)
	}
}

func TestUninstallPreservesForeignExecutableAndRejectsUnverifiedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "install")
	bin := filepath.Join(base, "bin")
	if _, err := Install(InstallOptions{
		BundleRoot: fixtureBundle(t, "0.7.0", "runtime-v1"), InstallRoot: root, SkipBinLink: true,
		CurrentOS: "testos", CurrentArch: "testarch",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(bin, "coconet")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Uninstall(UninstallOptions{InstallRoot: root, BinDirectory: bin})
	if err != nil || !result.RuntimeRemoved || result.ExecutableRemoved {
		t.Fatalf("unexpected uninstall with foreign executable %#v %v", result, err)
	}
	if content, err := os.ReadFile(foreign); err != nil || string(content) != "foreign" {
		t.Fatalf("foreign executable was changed: %q %v", content, err)
	}
	root = filepath.Join(base, "invalid-install")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "current"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(UninstallOptions{InstallRoot: root, BinDirectory: bin}); err == nil {
		t.Fatal("unverified install root was accepted")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("invalid root was removed: %v", err)
	}
}

func TestUninstallRemovesBrokenOwnedExecutableWhenRuntimeIsAbsent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "install")
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "coconet")
	if err := os.Symlink(filepath.Join(root, "current", "bin", "coconet"), link); err != nil {
		t.Fatal(err)
	}
	result, err := Uninstall(UninstallOptions{InstallRoot: root, BinDirectory: bin})
	if err != nil || !result.AlreadyAbsent || !result.ExecutableRemoved {
		t.Fatalf("unexpected absent uninstall %#v %v", result, err)
	}
}
