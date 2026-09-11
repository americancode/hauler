package cosign

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
)

// registryClientOpts is the small subset of Cosign's RegistryOptions needed
// by Hauler. Keeping this here avoids importing Cosign's CLI options package,
// which unconditionally pulls in the Azure credential helper.
func registryClientOpts(ctx context.Context, cfg Config) ([]ociremote.Option, error) {
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}

	tlsConfig, err := registryTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	opts = append(opts, remote.WithTransport(transport))

	// Reuse clients across the many registry operations performed by a
	// verifier, matching Cosign's RegistryOptions behavior.
	if pusher, err := remote.NewPusher(opts...); err == nil {
		opts = append(opts, remote.Reuse(pusher))
	}
	if puller, err := remote.NewPuller(opts...); err == nil {
		opts = append(opts, remote.Reuse(puller))
	}

	return []ociremote.Option{ociremote.WithRemoteOptions(opts...)}, nil
}

func registryTLSConfig(cfg Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipTLSVerify} //nolint:gosec // explicitly requested by the user
	if cfg.InsecureSkipTLSVerify || cfg.CaFile == "" {
		return tlsConfig, nil
	}

	caBytes, err := os.ReadFile(cfg.CaFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("no valid CA certs found in %s", cfg.CaFile)
	}
	tlsConfig.RootCAs = pool
	return tlsConfig, nil
}
