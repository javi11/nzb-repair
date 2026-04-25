package par2exedownloader

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	githubReleaseURL         = "https://api.github.com/repos/animetosho/par2cmdline-turbo/releases/latest"
	httpUserAgent            = "nzb-repair"
	maxReleaseResponseSize   = 1 << 20
	maxPar2AssetDownloadSize = 100 << 20
	maxPar2BinarySize        = 100 << 20
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// Release represents the structure of the GitHub release JSON response
type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// DownloadPar2Cmd downloads the latest par2cmd executable from GitHub releases
// for the current operating system and architecture.
//
// It performs the following steps:
// 1. Fetches latest release information from GitHub
// 2. Determines system OS and architecture
// 3. Finds appropriate release asset for the system
// 4. Downloads the executable file
//
// Returns:
//   - string: The name of the downloaded executable ("par2cmd")
//   - error: Any error encountered during the download process
func DownloadPar2Cmd() (string, error) {
	executable := par2CmdExecutableName()

	// Fetch the latest release information from GitHub API
	release, err := fetchLatestRelease()
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}

	// Determine the system's OS and architecture
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Map system details to the appropriate asset
	asset, err := findAssetForSystem(release, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("find par2cmd asset for %s/%s: %w", goos, goarch, err)
	}

	// Download the asset
	err = downloadAndInstallAsset(executable, asset)
	if err != nil {
		return "", fmt.Errorf("download par2cmd asset %s: %w", asset.Name, err)
	}

	slog.Info("Downloaded par2cmd successfully", "asset", asset.Name, "path", executable)

	return executable, nil
}

func par2CmdExecutableName() string {
	if runtime.GOOS == "windows" {
		return "par2cmd.exe"
	}

	return "par2cmd"
}

// fetchLatestRelease retrieves the latest release information from GitHub
func fetchLatestRelease() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, githubReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var release Release
	err = json.NewDecoder(io.LimitReader(resp.Body, maxReleaseResponseSize)).Decode(&release)
	if err != nil {
		return nil, err
	}

	return &release, nil
}

// findAssetForSystem matches the system's OS and architecture to an asset in the release
func findAssetForSystem(release *Release, goos, goarch string) (*struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, error) {
	var assetName string
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			assetName = "linux-amd64.zip"
		case "arm64":
			assetName = "linux-arm64.zip"
		case "arm":
			assetName = "linux-armhf.zip"
		default:
			return nil, fmt.Errorf("unsupported architecture: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "amd64":
			assetName = "macos-amd64.zip"
		case "arm64":
			assetName = "macos-arm64.zip"
		default:
			return nil, fmt.Errorf("unsupported architecture: %s", goarch)
		}
	case "windows":
		switch goarch {
		case "amd64":
			assetName = "win-x64.zip"
		case "arm64":
			assetName = "win-arm64.zip"
		default:
			return nil, fmt.Errorf("unsupported architecture: %s", goarch)
		}
	default:
		return nil, fmt.Errorf("unsupported operating system: %s", goos)
	}

	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, assetName) {
			return &asset, nil
		}
	}

	return nil, fmt.Errorf("no asset found for %s/%s", goos, goarch)
}

func downloadAndInstallAsset(filename string, asset *struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}) error {
	tmpDir := filepath.Dir(filename)
	tmpFile, err := os.CreateTemp(tmpDir, filepath.Base(filename)+".*.download")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", filename, err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", filename, err)
	}
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if err := downloadFile(tmpPath, asset.BrowserDownloadURL); err != nil {
		return err
	}

	if strings.HasSuffix(asset.Name, ".zip") {
		return installPar2CmdFromZip(tmpPath, filename)
	}

	return fmt.Errorf("unsupported par2cmd asset format: %s", asset.Name)
}

func installPar2CmdFromZip(archivePath, targetPath string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open par2cmd zip %s: %w", archivePath, err)
	}
	defer func() {
		_ = reader.Close()
	}()

	for _, file := range reader.File {
		base := path.Base(file.Name)
		if base != "par2" && base != "par2.exe" {
			continue
		}

		return extractZipFile(file, targetPath)
	}

	return fmt.Errorf("par2 executable not found in %s", archivePath)
}

func extractZipFile(file *zip.File, targetPath string) error {
	if file.UncompressedSize64 > maxPar2BinarySize {
		return fmt.Errorf("par2 binary %s exceeds maximum allowed size", file.Name)
	}

	in, err := file.Open()
	if err != nil {
		return fmt.Errorf("failed to open %s in zip: %w", file.Name, err)
	}
	defer func() {
		_ = in.Close()
	}()

	tmpDir := filepath.Dir(targetPath)
	tmpFile, err := os.CreateTemp(tmpDir, filepath.Base(targetPath)+".*.extract")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %s: %w", targetPath, err)
	}
	tmpPath := tmpFile.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	n, err := io.Copy(tmpFile, io.LimitReader(in, maxPar2BinarySize+1))
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to extract %s to %s: %w", file.Name, targetPath, err)
	}
	if n > maxPar2BinarySize {
		_ = tmpFile.Close()
		return fmt.Errorf("par2 binary %s exceeds maximum allowed size", file.Name)
	}

	// Chmod after writing so the final mode is not affected by the process umask.
	if err := tmpFile.Chmod(0755); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("error setting execute permission for %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close extracted par2 binary %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to install par2 binary to %s: %w", targetPath, err)
	}

	success = true
	return nil
}

// downloadFile downloads a file from the specified URL
func downloadFile(filename, url string) error {
	out, err := os.Create(filename)
	if err != nil {
		return err
	}

	defer func() {
		_ = out.Close()
	}()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	n, err := io.Copy(out, io.LimitReader(resp.Body, maxPar2AssetDownloadSize+1))
	if err != nil {
		return err
	}
	if n > maxPar2AssetDownloadSize {
		return fmt.Errorf("downloaded file exceeds maximum allowed size")
	}

	return nil
}
