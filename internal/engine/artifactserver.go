package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/nektos/act/pkg/common"
)

type endpoints struct {
	BindHost     string
	ExternalHost string
}

func containerEndpoints() (endpoints, error) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return endpoints{
			BindHost:     "0.0.0.0",
			ExternalHost: "host.docker.internal",
		}, nil
	}
	outbound := common.GetOutboundIP()
	if outbound == nil {
		return endpoints{}, errors.New("unable to determine a host address reachable from Docker")
	}
	host := outbound.String()
	return endpoints{BindHost: host, ExternalHost: host}, nil
}

func startArtifactProxy(ctx context.Context, bindHost string, port, backendPort int) (func(), error) {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", backendPort))
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
		http.Error(writer, proxyErr.Error(), http.StatusBadGateway)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		normalizeArtifactRequest(request)
		normalizeArtifactSignature(request)
		proxy.ServeHTTP(writer, request)
	})
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindHost, port))
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}, nil
}

func normalizeArtifactRequest(request *http.Request) {
	if request.Method != http.MethodPost ||
		!strings.HasSuffix(request.URL.Path, "/CreateArtifact") ||
		request.Body == nil {
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return
	}
	_ = request.Body.Close()
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		delete(payload, "mime_type")
		delete(payload, "mimeType")
		if normalized, marshalErr := json.Marshal(payload); marshalErr == nil {
			body = normalized
		}
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Length", fmt.Sprint(len(body)))
}

func normalizeArtifactSignature(request *http.Request) {
	endpoint := ""
	switch {
	case strings.HasSuffix(request.URL.Path, "/UploadArtifact"):
		endpoint = "UploadArtifact"
	case strings.HasSuffix(request.URL.Path, "/DownloadArtifact"):
		endpoint = "DownloadArtifact"
	default:
		return
	}
	query := request.URL.Query()
	expires := query.Get("expires")
	artifactName := query.Get("artifactName")
	taskID := query.Get("taskID")
	if expires == "" || artifactName == "" || taskID == "" {
		return
	}
	mac := hmac.New(sha256.New, []byte{0xba, 0xdb, 0xee, 0xf0})
	_, _ = io.WriteString(mac, endpoint)
	_, _ = io.WriteString(mac, expires)
	_, _ = io.WriteString(mac, artifactName)
	_, _ = io.WriteString(mac, taskID)
	query.Set("sig", base64.URLEncoding.EncodeToString(mac.Sum(nil)))
	request.URL.RawQuery = query.Encode()
}
