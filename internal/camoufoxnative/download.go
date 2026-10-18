package camoufoxnative

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const camoufoxRelease = "152.0.4-beta.29"

// installCamoufox downloads the Camoufox version aligned with the current protocol transport via the egress proxy.
func installCamoufox(ctx context.Context, executableName string, proxy string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	asset, err := camoufoxAssetName()
	if err != nil {
		return "", err
	}
	root, err := camoufoxInstallRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("creating Camoufox directory: %w", err)
	}
	archivePath := filepath.Join(filepath.Dir(root), asset+".part")
	address := fmt.Sprintf("https://github.com/daijro/camoufox/releases/download/v%s/%s", camoufoxRelease, asset)
	if err := downloadCamoufoxArchive(ctx, address, archivePath, proxy); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(root), ".camoufox-stage-*")
	if err != nil {
		return "", fmt.Errorf("creating Camoufox staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := extractCamoufoxArchive(ctx, archivePath, staging); err != nil {
		if ctx.Err() == nil {
			os.Remove(archivePath)
		}
		return "", err
	}
	stagedExecutable := filepath.Join(staging, executableName)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(stagedExecutable, 0o755); err != nil {
			return "", fmt.Errorf("setting Camoufox executable permissions: %w", err)
		}
	}
	if _, err := validateCamoufoxExecutable(stagedExecutable); err != nil {
		return "", fmt.Errorf("validating Camoufox staging directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.RemoveAll(root); err != nil {
		return "", fmt.Errorf("cleaning up old Camoufox directory: %w", err)
	}
	if err := os.Rename(staging, root); err != nil {
		return "", fmt.Errorf("publishing Camoufox directory: %w", err)
	}
	os.Remove(archivePath)
	executable := filepath.Join(root, executableName)
	slog.Info("Camoufox is ready", "path", executable)
	return executable, nil
}

// downloadCamoufoxArchive downloads release archive to archivePath via egress proxy, resuming from file offset if partial content already exists.
func downloadCamoufoxArchive(ctx context.Context, address string, archivePath string, proxy string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	egress := "direct"
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return fmt.Errorf("parsing Camoufox download proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		egress = proxyURL.Redacted()
	}
	var offset int64
	if info, err := os.Stat(archivePath); err == nil {
		offset = info.Size()
	}
	slog.Info("downloading Camoufox", "version", camoufoxRelease, "platform", runtime.GOOS+"/"+runtime.GOARCH, "egress", egress)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "AIStudio2API")
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := (&http.Client{Timeout: 30 * time.Minute, Transport: transport}).Do(request)
	if err != nil {
		return fmt.Errorf("downloading Camoufox: %w", err)
	}
	defer response.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch response.StatusCode {
	case http.StatusOK:
		offset = 0
		flags |= os.O_TRUNC
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusRequestedRangeNotSatisfiable:
		return nil
	default:
		return fmt.Errorf("downloading Camoufox: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > 0 {
		slog.Info("Camoufox download started", "size_mib", (offset+response.ContentLength)/(1024*1024), "resume_mib", offset/(1024*1024))
	}
	archive, err := os.OpenFile(archivePath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("creating Camoufox download file: %w", err)
	}
	_, copyErr := io.Copy(archive, contextReader{ctx: ctx, reader: response.Body})
	if closeErr := archive.Close(); copyErr != nil || closeErr != nil {
		return fmt.Errorf("saving Camoufox: %w", firstError(copyErr, closeErr))
	}
	return nil
}

func camoufoxInstallRoot() (string, error) {
	root, err := filepath.Abs(filepath.Join("runtime", "camoufox"))
	if err != nil {
		return "", fmt.Errorf("locating Camoufox directory: %w", err)
	}
	return root, nil
}

func camoufoxAssetName() (string, error) {
	platform := map[string]string{"windows": "win", "linux": "lin", "darwin": "mac"}[runtime.GOOS]
	architecture := map[string]string{"amd64": "x86_64", "386": "i686", "arm64": "arm64"}[runtime.GOARCH]
	if platform == "" || architecture == "" || runtime.GOOS == "darwin" && runtime.GOARCH == "386" {
		return "", fmt.Errorf("Camoufox release not available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("camoufox-%s-%s.%s.zip", camoufoxRelease, platform, architecture), nil
}

func extractCamoufoxArchive(ctx context.Context, archivePath string, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening Camoufox archive: %w", err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(entry.Name))
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("Camoufox archive contains invalid path %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, entry.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, entry.Mode())
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(targetFile, contextReader{ctx: ctx, reader: source})
		closeTargetErr := targetFile.Close()
		closeSourceErr := source.Close()
		if copyErr != nil || closeTargetErr != nil || closeSourceErr != nil {
			return fmt.Errorf("extracting Camoufox %s: %w", entry.Name, firstError(copyErr, closeTargetErr, closeSourceErr))
		}
	}
	return nil
}

// contextReader propagates cancellation during copy operations.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read checks for cancellation before each read operation.
func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}
