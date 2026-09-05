package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	commonParams "github.com/cloudbase/garm-provider-common/params"
)

type artifactTransport func(*http.Request) (*http.Response, error)

func (transport artifactTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestRunnerArtifactCacheVerification(t *testing.T) {
	for _, scenario := range []string{"verified", "upstream-corrupt", "missing-upstream-digest", "invalid-origin", "invalid-filename"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GARM_RUNNER_CACHE_DIR", t.TempDir())
			filename := "actions-runner-linux-x64-2.337.0.tar.gz"
			address := "https://github.com/actions/runner/releases/download/v2.337.0/" + filename
			tool := commonParams.RunnerApplicationDownload{Filename: &filename, DownloadURL: &address}
			payload := "verified runner release"
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
			var downloads atomic.Int32
			previousClient := artifactClient
			t.Cleanup(func() { artifactClient = previousClient })
			artifactClient = &http.Client{Transport: artifactTransport(func(request *http.Request) (*http.Response, error) {
				body := payload
				if request.URL.Host == "api.github.com" {
					metadataDigest := "sha256:" + digest
					if scenario == "missing-upstream-digest" {
						metadataDigest = ""
					}
					body = fmt.Sprintf(`{"assets":[{"name":%q,"browser_download_url":%q,"digest":%q}]}`, filename, address, metadataDigest)
				} else {
					downloads.Add(1)
					if scenario == "upstream-corrupt" {
						body = "corrupt"
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			if scenario == "invalid-origin" {
				address = "https://untrusted.example/runner.tar.gz"
			}
			if scenario == "invalid-filename" {
				filename = "../runner.tar.gz"
			}
			cached, actualDigest, err := RunnerArtifact(context.Background(), tool)
			if scenario != "verified" {
				if err == nil {
					t.Fatal("unsafe artifact accepted")
				}
				return
			}
			if err != nil || actualDigest != digest {
				t.Fatalf("cache preparation: digest=%s err=%v", actualDigest, err)
			}
			var group sync.WaitGroup
			for range 6 {
				group.Add(1)
				go func() {
					defer group.Done()
					if _, _, err := RunnerArtifact(context.Background(), tool); err != nil {
						t.Error(err)
					}
				}()
			}
			group.Wait()
			if downloads.Load() != 1 {
				t.Fatalf("cache hit downloaded upstream %d times", downloads.Load())
			}
			if err := os.WriteFile(cached, []byte("corrupt cache"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := RunnerArtifact(context.Background(), tool); err == nil {
				t.Fatal("corrupted local archive accepted")
			}
		})
	}
}
