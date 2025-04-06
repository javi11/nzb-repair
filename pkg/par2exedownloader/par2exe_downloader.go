package par2exedownloader

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
)

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
	executable := "par2cmd"

	// Fetch the latest release information from GitHub API
	release, err := fetchLatestRelease()
	if err != nil {
		slog.With("err", err).Error("Error fetching latest release")

		return "", err
	}

	// Determine the system's OS and architecture
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Map system details to the appropriate asset
	asset, err := findAssetForSystem(release, goos, goarch)
	if err != nil {
		slog.With("err", err).Error("Error finding asset for system")

		return "", err
	}

	// Download the asset
	err = downloadFile("par2cmd", asset.BrowserDownloadURL)
	if err != nil {
		slog.With("err", err).Error("Error downloading file")

		return "", err
	}

	slog.Info(fmt.Sprintf("Downloaded %s successfully.\n", asset.Name))

	return executable, nil
}

// fetchLatestRelease retrieves the latest release information from GitHub
func fetchLatestRelease() (*Release, error) {
	url := "https://api.github.com/repos/animetosho/par2cmdline-turbo/releases/latest"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var release Release
	err = json.NewDecoder(resp.Body).Decode(&release)
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
			assetName = "linux-amd64.xz"
		case "arm64":
			assetName = "linux-arm64.xz"
		case "arm":
			assetName = "linux-armhf.xz"
		default:
			return nil, fmt.Errorf("unsupported architecture: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "amd64":
			assetName = "macos-x64.xz"
		case "arm64":
			assetName = "macos-arm64.xz"
		default:
			return nil, fmt.Errorf("unsupported architecture: %s", goarch)
		}
	case "windows":
		switch goarch {
		case "amd64":
			assetName = "win-x64.zip"
		case "386":
			assetName = "win-x86.zip"
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

// downloadFile downloads a file from the specified URL
func downloadFile(filename, url string) error {
	out, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	// Add execute permissions to the downloaded file
	err = os.Chmod(filename, 0755)
	if err != nil {
		return fmt.Errorf("error setting execute permission for %s: %w", filename, err)
	}

	return nil
}
