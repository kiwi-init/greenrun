package engine

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeArtifactRequestDropsNewOptionalFields(t *testing.T) {
	request, err := http.NewRequest(
		http.MethodPost,
		"http://host/twirp/github.actions.results.api.v1.ArtifactService/CreateArtifact",
		strings.NewReader(`{"name":"result","mime_type":{"value":"application/zip"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	normalizeArtifactRequest(request)

	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "mime_type") {
		t.Fatalf("mime_type was not removed: %s", body)
	}
	if !strings.Contains(string(body), `"name":"result"`) {
		t.Fatalf("known fields were not preserved: %s", body)
	}
}

func TestNormalizeArtifactRequestLeavesOtherRoutesAlone(t *testing.T) {
	const original = `{"mime_type":"kept"}`
	request, err := http.NewRequest(http.MethodPost, "http://host/FinalizeArtifact", strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}

	normalizeArtifactRequest(request)

	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("body changed to %s", body)
	}
}

func TestNormalizeArtifactSignature(t *testing.T) {
	request, err := http.NewRequest(
		http.MethodPut,
		"http://host/twirp/github.actions.results.api.v1.ArtifactService/UploadArtifact?expires=soon&artifactName=result&taskID=1&sig=bad",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	normalizeArtifactSignature(request)

	signature := request.URL.Query().Get("sig")
	decoded, err := base64.URLEncoding.DecodeString(signature)
	if err != nil {
		t.Fatalf("signature is not URL-safe base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("signature has %d bytes, want 32", len(decoded))
	}
	if signature == "bad" {
		t.Fatal("signature was not replaced")
	}
	if _, err := url.Parse(request.URL.String()); err != nil {
		t.Fatalf("normalized URL is invalid: %v", err)
	}
}
