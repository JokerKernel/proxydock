package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvalidDownloadsNeverExecute(t *testing.T) {
	for _, test := range []struct{ name, body, message string }{
		{"empty", "", "为空"},
		{"html", "<html>not found</html>\n", "shebang"},
		{"nul", "#!/bin/bash\n\x00", "NUL"},
		{"encoding", "#!/bin/bash\n\xff", "UTF-8"},
		{"shebang", "#!/usr/bin/python3\nprint('wrong')\n", "shebang"},
		{"single line", "#!/bin/bash", "有效文本"},
		{"controls", "#!/bin/bash\n" + strings.Repeat("\x01", 100), "控制字符"},
		{"too large", "#!/bin/bash\n" + strings.Repeat("#", maxScriptSize), "超过"},
		{"syntax", "#!/bin/bash\nif then\n", "bash -n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerStub{}
			u := testUpdater(runner)
			u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return scriptResponse(req, test.body), nil
			})
			if err := u.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error=%v, want %q", err, test.message)
			}
			if runner.path != "" {
				t.Fatal("invalid script was executed")
			}
		})
	}
}

func TestDownloadFailureStopsUpdateAndUninstall(t *testing.T) {
	for _, uninstall := range []bool{false, true} {
		for _, failure := range []string{"network", "status", "read"} {
			t.Run(fmt.Sprintf("uninstall=%t/%s", uninstall, failure), func(t *testing.T) {
				runner := &runnerStub{}
				u := testUpdater(runner)
				want := errors.New("download unavailable")
				u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if failure == "network" {
						return nil, want
					}
					resp := scriptResponse(req, testScript)
					if failure == "status" {
						resp.StatusCode, resp.Status = 404, "404 Not Found"
					} else {
						resp.Body = io.NopCloser(failingReader{want})
					}
					return resp, nil
				})
				err := u.Run(context.Background(), Options{Uninstall: uninstall})
				if err == nil || runner.path != "" {
					t.Fatalf("download failure was ignored: %v", err)
				}
				if failure != "status" && !errors.Is(err, want) {
					t.Fatalf("error cause lost: %v", err)
				}
			})
		}
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestRejectsUnapprovedSourcesAndRedirects(t *testing.T) {
	for _, rawURL := range []string{"http://raw.githubusercontent.com/install.sh", "https://evil.example/install.sh", "https://raw.githubusercontent.com.evil.example/install.sh"} {
		u := testUpdater(&runnerStub{})
		u.ScriptURL = rawURL
		u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("unapproved URL was requested: %s", req.URL)
			return nil, nil
		})
		if err := u.Run(context.Background(), Options{}); err == nil {
			t.Fatalf("unapproved URL accepted: %s", rawURL)
		}
	}
	for _, redirect := range []string{"https://evil.example/install.sh", "http://github.com/install.sh", defaultScriptURL} {
		t.Run(redirect, func(t *testing.T) {
			runner := &runnerStub{}
			u := testUpdater(runner)
			requests := 0
			u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.String() != defaultScriptURL {
					t.Fatalf("unsafe redirect reached the network: %s", req.URL)
				}
				resp := scriptResponse(req, "")
				resp.StatusCode, resp.Status = 302, "302 Found"
				resp.Header.Set("Location", redirect)
				return resp, nil
			})
			if err := u.Run(context.Background(), Options{}); err == nil || runner.path != "" || requests > 5 {
				t.Fatalf("redirect protection failed: error=%v requests=%d", err, requests)
			}
			if u.Client.CheckRedirect != nil || u.Client.Timeout != 0 {
				t.Fatal("updater mutated the shared HTTP client")
			}
		})
	}
}

func TestApprovedRedirectReportsFinalSourceAndHash(t *testing.T) {
	runner := &runnerStub{}
	u := testUpdater(runner)
	var output bytes.Buffer
	u.Stderr = &output
	requests := 0
	finalURL := "https://github.com/JokerKernel/proxydock/raw/main/scripts/install.sh"
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			resp := scriptResponse(req, "")
			resp.StatusCode, resp.Status = 302, "302 Found"
			resp.Header.Set("Location", finalURL)
			return resp, nil
		}
		return scriptResponse(req, testScript), nil
	})
	if err := u.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || runner.body != testScript || !strings.Contains(output.String(), finalURL) || !strings.Contains(output.String(), fmt.Sprintf("%x", sha256.Sum256([]byte(testScript)))) {
		t.Fatalf("download provenance missing: requests=%d output=%s", requests, output.String())
	}
}

func TestCancellationDuringDownloadNeverExecutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	runner := &runnerStub{}
	u := testUpdater(runner)
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	if err := u.Run(ctx, Options{}); !errors.Is(err, context.DeadlineExceeded) || runner.path != "" {
		t.Fatalf("download cancellation failed: %v", err)
	}
}

func TestDownloadedScriptRunsWithChildVersionOnly(t *testing.T) {
	t.Setenv("PROXYDOCK_VERSION", "parent")
	u := testUpdater(nil)
	var stdout, stderr bytes.Buffer
	u.Stdout, u.Stderr = &stdout, &stderr
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return scriptResponse(req, "#!/bin/bash\nprintf '%s' \"$PROXYDOCK_VERSION\"\n"), nil
	})
	if err := u.Run(context.Background(), Options{Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "v1.2.3" || os.Getenv("PROXYDOCK_VERSION") != "parent" {
		t.Fatalf("incorrect version environment: output=%q parent=%q", stdout.String(), os.Getenv("PROXYDOCK_VERSION"))
	}
}

func TestSyntaxValidationDoesNotExecuteDownloadedCommands(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	runner := &runnerStub{}
	u := testUpdater(runner)
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return scriptResponse(req, "#!/bin/bash\ntouch '"+marker+"'\nif then\n"), nil
	})
	if err := u.Run(context.Background(), Options{}); err == nil {
		t.Fatal("invalid syntax accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) || runner.path != "" {
		t.Fatal("syntax checking executed downloaded commands")
	}
}
