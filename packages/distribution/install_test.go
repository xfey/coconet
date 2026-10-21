package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallIsAtomicIdempotentAndKeepsPreviousRelease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	bin := filepath.Join(t.TempDir(), "bin")
	previousBundle := fixtureBundle(t, "0.7.0", "runtime-v1")
	options := InstallOptions{BundleRoot: previousBundle, InstallRoot: root, BinDirectory: bin, CurrentOS: "testos", CurrentArch: "testarch"}
	first, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if first.AlreadyExists || first.Version != "0.7.0" {
		t.Fatalf("unexpected first install %#v", first)
	}
	content, err := os.ReadFile(first.Executable)
	if err != nil || string(content) != "runtime-v1" {
		t.Fatalf("installed executable mismatch: %q %v", content, err)
	}
	second, err := Install(options)
	if err != nil || !second.AlreadyExists || second.ReleasePath != first.ReleasePath {
		t.Fatalf("idempotent install mismatch %#v %v", second, err)
	}
	previousRelease := first.ReleasePath
	options.BundleRoot = fixtureBundle(t, "0.8.0", "runtime-v2")
	upgraded, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(upgraded.Executable)
	if err != nil || string(content) != "runtime-v2" {
		t.Fatalf("upgraded executable mismatch: %q %v", content, err)
	}
	if _, err := os.Stat(previousRelease); err != nil {
		t.Fatalf("previous release was not retained: %v", err)
	}
	options.BundleRoot = previousBundle
	rolledBack, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(rolledBack.Executable)
	if err != nil || string(content) != "runtime-v1" || !rolledBack.AlreadyExists {
		t.Fatalf("rollback did not reactivate the retained release: %#v %q %v", rolledBack, content, err)
	}
	if _, err := os.Stat(upgraded.ReleasePath); err != nil {
		t.Fatalf("newer release was not retained after rollback: %v", err)
	}
}

func TestInstallRejectsTamperPlatformAndForeignExecutable(t *testing.T) {
	base := t.TempDir()
	bundle := fixtureBundle(t, "0.7.0", "runtime-v1")
	options := InstallOptions{BundleRoot: bundle, InstallRoot: filepath.Join(base, "install"), BinDirectory: filepath.Join(base, "bin"), CurrentOS: "other", CurrentArch: "testarch"}
	if _, err := Install(options); err == nil {
		t.Fatal("platform mismatch was accepted")
	}
	options.CurrentOS = "testos"
	if err := os.MkdirAll(options.BinDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.BinDirectory, "coconet"), []byte("foreign"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(options); err == nil {
		t.Fatal("foreign executable was replaced")
	}
	if _, err := os.Lstat(filepath.Join(options.InstallRoot, "current")); !os.IsNotExist(err) {
		t.Fatalf("failed preflight changed current release: %v", err)
	}
	if err := os.Remove(filepath.Join(options.BinDirectory, "coconet")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "bin", "coconet"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(options); err == nil {
		t.Fatal("tampered bundle was installed")
	}
}

func TestInstallCanLeaveTheCommandNameToAPackageManager(t *testing.T) {
	base := t.TempDir()
	installRoot := filepath.Join(base, "install")
	result, err := Install(InstallOptions{
		BundleRoot:  fixtureBundle(t, "0.7.0", "runtime-v1"),
		InstallRoot: installRoot,
		SkipBinLink: true,
		CurrentOS:   "testos",
		CurrentArch: "testarch",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(installRoot, "current", "bin", "coconet")
	if result.Executable != expected {
		t.Fatalf("unexpected Runtime executable %q", result.Executable)
	}
	content, err := os.ReadFile(result.Executable)
	if err != nil || string(content) != "runtime-v1" {
		t.Fatalf("installed Runtime mismatch: %q %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(base, "bin")); !os.IsNotExist(err) {
		t.Fatalf("skip-bin-link unexpectedly created a command directory: %v", err)
	}
}

func TestStageOnlyPreservesActiveRuntime(t *testing.T) {
	root := t.TempDir()
	options := InstallOptions{BundleRoot: fixtureBundle(t, "1.0.0", "old"), InstallRoot: root, SkipBinLink: true, CurrentOS: "testos", CurrentArch: "testarch"}
	old, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	options.BundleRoot = fixtureBundle(t, "1.1.0", "new")
	options.StageOnly = true
	staged, err := Install(options)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(old.Executable)
	if err != nil || string(data) != "old" {
		t.Fatal("staging changed the active Runtime", err)
	}
	if _, err := os.Stat(filepath.Join(staged.ReleasePath, "bin", "coconet")); err != nil {
		t.Fatal(err)
	}
	options.StageOnly = false
	if _, err := Install(options); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(old.Executable)
	if err != nil || string(data) != "new" {
		t.Fatal("activation failed", err)
	}
}
