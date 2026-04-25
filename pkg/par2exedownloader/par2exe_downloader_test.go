package par2exedownloader

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindAssetForSystemFindsCurrentMacOSARM64Zip(t *testing.T) {
	release := &Release{
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{
				Name:               "par2cmdline-turbo-1.4.0-macos-arm64.zip",
				BrowserDownloadURL: "https://example.com/par2cmdline-turbo-1.4.0-macos-arm64.zip",
			},
		},
	}

	asset, err := findAssetForSystem(release, "darwin", "arm64")
	if err != nil {
		t.Fatalf("findAssetForSystem() error = %v", err)
	}

	if asset.Name != "par2cmdline-turbo-1.4.0-macos-arm64.zip" {
		t.Fatalf("findAssetForSystem() asset = %q, want macOS arm64 zip", asset.Name)
	}
}

func TestInstallPar2CmdFromZipExtractsPar2AsPar2Cmd(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "par2.zip")
	targetPath := filepath.Join(tmpDir, "par2cmd")

	createTestZip(t, archivePath, "bin/par2", []byte("#!/bin/sh\nexit 0\n"))

	if err := installPar2CmdFromZip(archivePath, targetPath); err != nil {
		t.Fatalf("installPar2CmdFromZip() error = %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", targetPath, err)
	}
	if string(got) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("installed file content = %q, want zip par2 content", string(got))
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", targetPath, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("installed file mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestInstallPar2CmdFromZipReturnsErrorWhenPar2IsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "par2.zip")
	targetPath := filepath.Join(tmpDir, "par2cmd")

	createTestZip(t, archivePath, "README.txt", []byte("not a binary"))

	err := installPar2CmdFromZip(archivePath, targetPath)
	if err == nil {
		t.Fatal("installPar2CmdFromZip() error = nil, want par2 executable not found")
	}
	if !strings.Contains(err.Error(), "par2 executable not found") {
		t.Fatalf("installPar2CmdFromZip() error = %q, want par2 executable not found", err)
	}
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Fatalf("Stat(%q) error = %v, want not exist", targetPath, statErr)
	}
}

func createTestZip(t *testing.T, path, name string, content []byte) {
	t.Helper()

	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", path, err)
	}
	defer func() {
		if err := out.Close(); err != nil {
			t.Fatalf("Close(%q) error = %v", path, err)
		}
	}()

	zipWriter := zip.NewWriter(out)
	defer func() {
		if err := zipWriter.Close(); err != nil {
			t.Fatalf("zip Close() error = %v", err)
		}
	}()

	writer, err := zipWriter.Create(name)
	if err != nil {
		t.Fatalf("zip Create(%q) error = %v", name, err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("zip Write(%q) error = %v", name, err)
	}
}
