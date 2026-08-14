package commands

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	updateRepoOwner = "collectiveai-team"
	updateRepoName  = "orquesta-lite"
	updateBinary    = "orq-lite"
)

type UpdateOptions struct {
	CurrentVersion string
	CheckOnly      bool
	Out            io.Writer
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

func Update(ctx context.Context, opts UpdateOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	fmt.Fprintf(opts.Out, "current: %s\nlatest:  %s\n", opts.CurrentVersion, rel.TagName)

	if !shouldUpdate(opts.CurrentVersion, rel.TagName) {
		fmt.Fprintln(opts.Out, "already up to date")
		return nil
	}
	if opts.CheckOnly {
		fmt.Fprintf(opts.Out, "update available: %s\nrun `orq-lite update` to install\n", rel.HTMLURL)
		return nil
	}

	archiveName, sumName := assetNames(rel.TagName, runtime.GOOS, runtime.GOARCH)
	archiveURL, sumURL, err := pickAssetURLs(rel, archiveName, sumName)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "orq-lite-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, archiveName)
	if err := downloadFile(ctx, archiveURL, archivePath); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	sumPath := filepath.Join(tmpDir, sumName)
	if err := downloadFile(ctx, sumURL, sumPath); err != nil {
		return fmt.Errorf("download checksum: %w", err)
	}
	if err := verifySHA256(archivePath, sumPath); err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}

	binName := updateBinary
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	extractedBin := filepath.Join(tmpDir, binName)
	if err := extractBinary(archivePath, binName, extractedBin); err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	target, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}

	if err := replaceBinary(target, extractedBin); err != nil {
		return fmt.Errorf("install new binary: %w", err)
	}
	fmt.Fprintf(opts.Out, "installed %s at %s\n", rel.TagName, target)
	return nil
}

func shouldUpdate(current, latest string) bool {
	if current == "" || current == "dev" {
		return true
	}
	return strings.TrimPrefix(current, "v") != strings.TrimPrefix(latest, "v")
}

func assetNames(tag, goos, goarch string) (archive, sum string) {
	base := fmt.Sprintf("%s-%s-%s-%s", updateBinary, tag, goos, goarch)
	if goos == "windows" {
		archive = base + ".zip"
	} else {
		archive = base + ".tar.gz"
	}
	sum = archive + ".sha256"
	return
}

func pickAssetURLs(rel *ghRelease, archiveName, sumName string) (archiveURL, sumURL string, err error) {
	for _, a := range rel.Assets {
		switch a.Name {
		case archiveName:
			archiveURL = a.BrowserDownloadURL
		case sumName:
			sumURL = a.BrowserDownloadURL
		}
	}
	if archiveURL == "" {
		return "", "", fmt.Errorf("release %s has no asset %q (unsupported platform?)", rel.TagName, archiveName)
	}
	if sumURL == "" {
		return "", "", fmt.Errorf("release %s has no checksum %q", rel.TagName, sumName)
	}
	return archiveURL, sumURL, nil
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", updateRepoOwner, updateRepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("github api returned empty tag_name")
	}
	return &rel, nil
}

func downloadFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func verifySHA256(file, sumFile string) error {
	raw, err := os.ReadFile(sumFile)
	if err != nil {
		return err
	}
	expected := strings.TrimSpace(strings.Fields(string(raw))[0])

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}

func extractBinary(archivePath, binName, dst string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractFromZip(archivePath, binName, dst)
	}
	return extractFromTarGz(archivePath, binName, dst)
}

func extractFromTarGz(archivePath, binName, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(hdr.Name) != binName {
			continue
		}
		return writeBinary(dst, tr, 0o755)
	}
	return fmt.Errorf("binary %q not found in archive", binName)
}

func extractFromZip(archivePath, binName, dst string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if path.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeBinary(dst, rc, 0o755)
		rc.Close()
		return err
	}
	return fmt.Errorf("binary %q not found in archive", binName)
}

func writeBinary(dst string, src io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// replaceBinary atomically swaps the running binary with the freshly-downloaded one.
// On Unix, os.Rename in the same directory is atomic and works even on the running
// executable. On Windows, the running .exe cannot be deleted, so we rename it to
// <exe>.old first; the .old file can be removed on the next run.
func replaceBinary(target, newBin string) error {
	dir := filepath.Dir(target)
	staged := filepath.Join(dir, filepath.Base(target)+".new")
	if err := copyFile(newBin, staged, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := target + ".old"
		_ = os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			_ = os.Remove(staged)
			return err
		}
		if err := os.Rename(staged, target); err != nil {
			_ = os.Rename(old, target)
			return err
		}
		return nil
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
