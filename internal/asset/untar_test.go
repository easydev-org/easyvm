package asset

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// THE FIRMWARE ARTIFACT IS A TAR, and this pins the three names the engine reads out of it.
//
// MEASURED: the build script writes `macos.vars.template` and the engine copies it to
// `macos.vars`, because qemu WRITES to that file and the artifact must carry the untouched
// template. A rename on one side is a stage that boots with an empty NVRAM and cannot say why.
func TestTheFirmwareTarUnpacksItsThreeFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "macos-firmware.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for name, body := range map[string]string{
		"boot.img":            "boot",
		"macos.rom":           "rom",
		"macos.vars.template": "vars",
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := untar(archive, out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"boot.img", "macos.rom", "macos.vars.template"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s did not unpack: %v", name, err)
		}
	}
}

// AND A PATH IN THE TAR CANNOT ESCAPE THE DIRECTORY. An artifact is data from a registry, and data
// that can write `../../etc/cron.d/x` is a supply-chain hole rather than a convenience.
func TestATarPathCannotEscape(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar")
	f, _ := os.Create(archive)
	tw := tar.NewWriter(f)
	body := "x"
	_ = tw.WriteHeader(&tar.Header{Name: "../../escaped", Mode: 0o644,
		Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = f.Close()

	out := filepath.Join(dir, "out")
	_ = os.MkdirAll(out, 0o755)
	if err := untar(archive, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
		t.Fatal("a tar entry wrote outside the destination directory")
	}
}
