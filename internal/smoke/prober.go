package smoke

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"context"
)

// maxProbeBytes bounds what the macOS probe reads. The marker is one short
// line; anything larger is a misbehaving upstream, not a longer answer.
const maxProbeBytes = 64 << 10

// HTTPProber reads the marker from macOS through the published guest port.
type HTTPProber struct {
	Client *http.Client
}

func (prober HTTPProber) Get(ctx context.Context, url string) (string, error) {
	client := prober.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build the request for %s: %w", url, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", url, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, response.StatusCode)
	}
	return string(body), nil
}
