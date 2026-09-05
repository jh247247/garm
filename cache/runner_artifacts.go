package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	commonParams "github.com/cloudbase/garm-provider-common/params"
)

var artifactName = regexp.MustCompile(`^actions-runner-linux-x64-([0-9]+\.[0-9]+\.[0-9]+)\.tar\.gz$`)
var artifactDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var artifactLock = make(chan struct{}, 1)
var artifactClient = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || request.URL.Scheme != "https" {
			return fmt.Errorf("unsafe runner artifact redirect")
		}
		switch request.URL.Hostname() {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		default:
			return fmt.Errorf("unexpected runner artifact redirect origin")
		}
	},
}

func RunnerArtifact(ctx context.Context, tool commonParams.RunnerApplicationDownload) (string, string, error) {
	directory := os.Getenv("GARM_RUNNER_CACHE_DIR")
	if directory == "" || !filepath.IsAbs(directory) {
		return "", "", fmt.Errorf("runner artifact cache is not configured")
	}
	filename := tool.GetFilename()
	match := artifactName.FindStringSubmatch(filename)
	if match == nil {
		return "", "", fmt.Errorf("unsupported runner artifact identity")
	}
	upstream := "https://github.com/actions/runner/releases/download/v" + match[1] + "/" + filename
	if tool.GetDownloadURL() != upstream {
		return "", "", fmt.Errorf("runner artifact origin does not match its identity")
	}
	// ponytail: serialize release downloads; use per-package locks if architectures expand.
	select {
	case artifactLock <- struct{}{}:
		defer func() { <-artifactLock }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", "", err
	}
	filename = filepath.Join(directory, filename)
	digest := tool.GetSHA256Checksum()
	if digest != "" && !artifactDigest.MatchString(digest) {
		return "", "", fmt.Errorf("invalid upstream runner checksum")
	}
	if cachedDigest, err := os.ReadFile(filename + ".sha256"); err == nil {
		stored := strings.TrimSpace(string(cachedDigest))
		if !artifactDigest.MatchString(stored) || (digest != "" && digest != stored) {
			return "", "", fmt.Errorf("cached runner checksum metadata mismatch")
		}
		if err := verifyArtifact(filename, stored); err != nil {
			return "", "", err
		}
		return filename, stored, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if digest == "" {
		response, err := artifactGet(ctx, "https://api.github.com/repos/actions/runner/releases/tags/v"+match[1])
		if err != nil {
			return "", "", err
		}
		var release struct {
			Assets []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
				URL    string `json:"browser_download_url"`
			} `json:"assets"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release)
		response.Body.Close()
		if err != nil {
			return "", "", fmt.Errorf("invalid upstream release metadata")
		}
		for _, asset := range release.Assets {
			if asset.Name == tool.GetFilename() && asset.URL == upstream {
				digest = strings.TrimPrefix(asset.Digest, "sha256:")
			}
		}
		if !artifactDigest.MatchString(digest) {
			return "", "", fmt.Errorf("upstream release lacks a verified runner checksum")
		}
	}
	response, err := artifactGet(ctx, upstream)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	temporary, err := os.CreateTemp(directory, ".runner-download-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(temporary.Name())
	written, copyErr := io.Copy(temporary, io.LimitReader(response.Body, (1<<30)+1))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil || written > 1<<30 {
		return "", "", fmt.Errorf("runner archive download failed or exceeded size limit")
	}
	if err := verifyArtifact(temporary.Name(), digest); err != nil {
		return "", "", err
	}
	if err := os.Rename(temporary.Name(), filename); err != nil {
		return "", "", err
	}
	marker, err := os.CreateTemp(directory, ".runner-checksum-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(marker.Name())
	_, writeErr := marker.WriteString(digest + "\n")
	closeErr = marker.Close()
	if writeErr != nil || closeErr != nil {
		return "", "", fmt.Errorf("runner checksum publication failed")
	}
	if err := os.Rename(marker.Name(), filename+".sha256"); err != nil {
		return "", "", err
	}
	return filename, digest, nil
}

func artifactGet(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := artifactClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("runner artifact upstream request failed")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("runner artifact upstream returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func verifyArtifact(filename, expected string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("runner archive checksum mismatch")
	}
	return nil
}
