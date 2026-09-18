package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	goname "github.com/google/go-containerregistry/pkg/name"
	gv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"hauler.dev/go/hauler/v2/pkg/artifacts"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/registry"

	"hauler.dev/go/hauler/v2/pkg/consts"
	"hauler.dev/go/hauler/v2/pkg/layer"
)

var (
	_             artifacts.OCI  = (*Chart)(nil)
	settings                     = cli.New()
	chartKeychain authn.Keychain = authn.DefaultKeychain
)

// resolveRepoCredentials bridges Docker credentials (including those written
// by `hauler login`) to Helm's legacy HTTP chart-repository downloader. Helm's
// OCI client reads the Docker keychain itself, but the legacy downloader only
// knows about ChartPathOptions.Username and Password.
func resolveRepoCredentials(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", nil
	}
	reg, err := goname.NewRegistry(u.Host)
	if err != nil {
		return "", "", fmt.Errorf("parsing chart repository host %q: %w", u.Host, err)
	}
	auth, err := chartKeychain.Resolve(reg)
	if err != nil {
		return "", "", fmt.Errorf("resolving chart repository credentials for %q: %w", u.Host, err)
	}
	if auth == authn.Anonymous {
		return "", "", nil
	}
	config, err := auth.Authorization()
	if err != nil {
		return "", "", fmt.Errorf("reading chart repository credentials for %q: %w", u.Host, err)
	}
	return config.Username, config.Password, nil
}

// chart implements the oci interface for chart api objects... api spec values are stored into the name, repo, and version fields
type Chart struct {
	path        string
	annotations map[string]string
	// preservedManifest is populated when the source is an OCI registry. In
	// that case the chart must be relocated as an OCI artifact, not rebuilt
	// from the downloaded archive: Helm's manifest annotations are part of the
	// manifest digest.
	preservedManifest     *gv1.Manifest
	preservedManifestData []byte
	preservedConfig       []byte
	preservedLayers       []preservedLayer
}

type preservedLayer struct {
	data        []byte
	mediaType   string
	annotations map[string]string
}

// newchart is a helper method that returns newlocalchart or newremotechart depending on chart contents
func NewChart(name string, opts *action.ChartPathOptions) (*Chart, error) {
	chartRef := name
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), settings.Namespace(), os.Getenv("HELM_DRIVER")); err != nil {
		return nil, err
	}

	client := action.NewInstall(actionConfig)

	// Propagate auth, TLS, and verification options from the caller.
	// RepoURL is intentionally NOT copied here — it is set conditionally below
	// based on URL scheme (OCI vs HTTP vs bare).
	client.ChartPathOptions.Version = opts.Version
	client.ChartPathOptions.Verify = opts.Verify
	client.ChartPathOptions.Keyring = opts.Keyring
	client.ChartPathOptions.Username = opts.Username
	client.ChartPathOptions.Password = opts.Password
	client.ChartPathOptions.PassCredentialsAll = opts.PassCredentialsAll
	client.ChartPathOptions.CertFile = opts.CertFile
	client.ChartPathOptions.KeyFile = opts.KeyFile
	client.ChartPathOptions.CaFile = opts.CaFile
	client.ChartPathOptions.InsecureSkipTLSVerify = opts.InsecureSkipTLSVerify
	client.ChartPathOptions.PlainHTTP = opts.PlainHTTP

	registryClient, err := newRegistryClient(client.CertFile, client.KeyFile, client.CaFile,
		client.InsecureSkipTLSVerify, client.PlainHTTP)
	if err != nil {
		return nil, fmt.Errorf("missing registry client: %w", err)
	}

	client.SetRegistryClient(registryClient)
	if registry.IsOCI(opts.RepoURL) {
		chartRef = opts.RepoURL + "/" + name
	} else if isUrl(opts.RepoURL) { // oci protocol registers as a valid url
		if client.ChartPathOptions.Username == "" && client.ChartPathOptions.Password == "" {
			username, password, err := resolveRepoCredentials(opts.RepoURL)
			if err != nil {
				return nil, err
			}
			client.ChartPathOptions.Username = username
			client.ChartPathOptions.Password = password
		}
		client.ChartPathOptions.RepoURL = opts.RepoURL
	} else { // handles cases like grafana and loki
		chartRef = opts.RepoURL + "/" + name
	}

	chartPath, err := client.ChartPathOptions.LocateChart(chartRef, settings)
	if err != nil {
		return nil, err
	}

	h := &Chart{
		path: chartPath,
	}

	// LocateChart gives us the chart archive, which is sufficient for normal
	// chart sources but not for digest-preserving OCI relocation. Pull the OCI
	// manifest and its blobs as well so Manifest/Layers can reproduce the
	// original descriptor graph byte-for-byte.
	if registry.IsOCI(opts.RepoURL) {
		pulled, err := registryClient.Pull(chartRef,
			registry.PullOptWithChart(true),
			registry.PullOptWithProv(true),
			registry.PullOptIgnoreMissingProv(true),
		)
		if err != nil {
			return nil, fmt.Errorf("preserving OCI chart manifest: %w", err)
		}

		var manifest gv1.Manifest
		if err := json.Unmarshal(pulled.Manifest.Data, &manifest); err != nil {
			return nil, fmt.Errorf("decoding OCI chart manifest: %w", err)
		}
		h.preservedManifest = &manifest
		h.preservedManifestData = append([]byte(nil), pulled.Manifest.Data...)
		h.preservedConfig = append([]byte(nil), pulled.Config.Data...)

		for _, descriptor := range manifest.Layers {
			var data []byte
			switch descriptor.Digest.String() {
			case pulled.Chart.Digest:
				data = pulled.Chart.Data
			case pulled.Prov.Digest:
				data = pulled.Prov.Data
			}
			if data == nil {
				return nil, fmt.Errorf("OCI chart layer %s was not returned by Helm", descriptor.Digest)
			}
			h.preservedLayers = append(h.preservedLayers, preservedLayer{
				data:        append([]byte(nil), data...),
				mediaType:   string(descriptor.MediaType),
				annotations: descriptor.Annotations,
			})
		}
	}

	return h, nil
}

func (h *Chart) MediaType() string {
	return consts.OCIManifestSchema1
}

func (h *Chart) Manifest() (*gv1.Manifest, error) {
	if h.preservedManifest != nil {
		manifest := *h.preservedManifest
		manifest.Config.Data = nil
		for i := range manifest.Layers {
			manifest.Layers[i].Data = nil
		}
		return &manifest, nil
	}

	cfgDesc, err := h.configDescriptor()
	if err != nil {
		return nil, err
	}

	var layerDescs []gv1.Descriptor
	ls, err := h.Layers()
	if err != nil {
		return nil, err
	}
	for _, l := range ls {
		desc, err := partial.Descriptor(l)
		if err != nil {
			return nil, err
		}
		// Helm's OCI pusher does not add the local archive filename to the
		// chart layer descriptor. Keep that annotation on the Hauler layer
		// object for extraction, but omit it from the pushed manifest so the
		// manifest matches Helm's.
		desc.Annotations = nil
		layerDescs = append(layerDescs, *desc)
	}

	ch, err := loader.Load(h.path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(h.path)
	if err != nil {
		return nil, err
	}

	return &gv1.Manifest{
		SchemaVersion: 2,
		// Helm's registry client tags the manifest with this media type in
		// ORAS, but omits the field from the serialized manifest JSON.
		MediaType:   "",
		Config:      cfgDesc,
		Layers:      layerDescs,
		Annotations: helmOCIAnnotations(ch.Metadata, info.ModTime().Format(time.RFC3339)),
	}, nil
}

// RawManifest returns the exact source manifest for OCI charts. For archive
// charts it emits the Helm-compatible descriptor field order; OCI manifest
// JSON field order is part of the content digest.
func (h *Chart) RawManifest() ([]byte, error) {
	if h.preservedManifestData != nil {
		return append([]byte(nil), h.preservedManifestData...), nil
	}

	manifest, err := h.Manifest()
	if err != nil {
		return nil, err
	}
	type helmManifest struct {
		SchemaVersion int64                `json:"schemaVersion"`
		Config        ocispec.Descriptor   `json:"config"`
		Layers        []ocispec.Descriptor `json:"layers"`
		Annotations   map[string]string    `json:"annotations,omitempty"`
	}
	config := ocispec.Descriptor{
		MediaType: string(manifest.Config.MediaType),
		Digest:    digest.Digest(manifest.Config.Digest.String()),
		Size:      manifest.Config.Size,
	}
	layers := make([]ocispec.Descriptor, 0, len(manifest.Layers))
	for _, descriptor := range manifest.Layers {
		layers = append(layers, ocispec.Descriptor{
			MediaType:   string(descriptor.MediaType),
			Digest:      digest.Digest(descriptor.Digest.String()),
			Size:        descriptor.Size,
			Annotations: descriptor.Annotations,
		})
	}
	return json.Marshal(helmManifest{
		SchemaVersion: manifest.SchemaVersion,
		Config:        config,
		Layers:        layers,
		Annotations:   manifest.Annotations,
	})
}

// helmOCIAnnotations mirrors Helm's registry.generateOCIAnnotations. These
// annotations are part of the manifest digest, so chart archives must use the
// same rules as Helm when Hauler creates an OCI manifest.
func helmOCIAnnotations(meta *v2.Metadata, creationTime string) map[string]string {
	annotations := make(map[string]string)
	add := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			annotations[key] = value
		}
	}

	add(ocispec.AnnotationDescription, meta.Description)
	add(ocispec.AnnotationTitle, meta.Name)
	add(ocispec.AnnotationVersion, meta.Version)
	add(ocispec.AnnotationURL, meta.Home)
	add(ocispec.AnnotationCreated, creationTime)
	if len(meta.Sources) > 0 {
		add(ocispec.AnnotationSource, meta.Sources[0])
	}
	if len(meta.Maintainers) > 0 {
		var maintainers strings.Builder
		for i, maintainer := range meta.Maintainers {
			if maintainer.Name != "" {
				maintainers.WriteString(maintainer.Name)
			}
			if maintainer.Email != "" {
				maintainers.WriteString(" (")
				maintainers.WriteString(maintainer.Email)
				maintainers.WriteString(")")
			}
			if i < len(meta.Maintainers)-1 {
				maintainers.WriteString(", ")
			}
		}
		add(ocispec.AnnotationAuthors, maintainers.String())
	}

	// Helm copies Chart.yaml annotations, except for the immutable OCI
	// identity fields which Helm generated above.
	for key, value := range meta.Annotations {
		if key == ocispec.AnnotationVersion || key == ocispec.AnnotationTitle {
			continue
		}
		annotations[key] = value
	}
	return annotations
}

func (h *Chart) RawConfig() ([]byte, error) {
	if h.preservedConfig != nil {
		return append([]byte(nil), h.preservedConfig...), nil
	}

	ch, err := loader.Load(h.path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ch.Metadata)
}

func (h *Chart) configDescriptor() (gv1.Descriptor, error) {
	data, err := h.RawConfig()
	if err != nil {
		return gv1.Descriptor{}, err
	}

	hash, size, err := gv1.SHA256(bytes.NewBuffer(data))
	if err != nil {
		return gv1.Descriptor{}, err
	}

	return gv1.Descriptor{
		MediaType: consts.ChartConfigMediaType,
		Size:      size,
		Digest:    hash,
	}, nil
}

func (h *Chart) Load() (*v2.Chart, error) {
	return loader.Load(h.path)
}

func (h *Chart) Layers() ([]gv1.Layer, error) {
	if h.preservedManifest != nil {
		layers := make([]gv1.Layer, 0, len(h.preservedLayers))
		for _, preserved := range h.preservedLayers {
			data := append([]byte(nil), preserved.data...)
			lyr, err := layer.FromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(data)), nil
			}, layer.WithMediaType(preserved.mediaType), layer.WithAnnotations(preserved.annotations))
			if err != nil {
				return nil, err
			}
			layers = append(layers, lyr)
		}
		return layers, nil
	}

	chartDataLayer, err := h.chartData()
	if err != nil {
		return nil, err
	}

	return []gv1.Layer{
		chartDataLayer,
		// TODO: Add provenance
	}, nil
}

func (h *Chart) RawChartData() ([]byte, error) {
	return os.ReadFile(h.path)
}

// chartdata loads the chart contents into memory and returns a NopCloser for the contents
// normally we avoid loading into memory, but charts sizes are strictly capped at ~1MB
func (h *Chart) chartData() (gv1.Layer, error) {
	info, err := os.Stat(h.path)
	if err != nil {
		return nil, err
	}

	var chartdata []byte
	if info.IsDir() {
		buf := &bytes.Buffer{}
		gw := gzip.NewWriter(buf)
		tw := tar.NewWriter(gw)

		if err := filepath.WalkDir(h.path, func(path string, d fs.DirEntry, err error) error {
			fi, err := d.Info()
			if err != nil {
				return err
			}

			header, err := tar.FileInfoHeader(fi, fi.Name())
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(filepath.Dir(h.path), path)
			if err != nil {
				return err
			}
			header.Name = rel

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if !d.IsDir() {
				data, err := os.Open(path)
				if err != nil {
					return err
				}
				if _, err := io.Copy(tw, data); err != nil {
					return err
				}
			}

			return nil
		}); err != nil {
			return nil, err
		}

		if err := tw.Close(); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		chartdata = buf.Bytes()

	} else {
		data, err := os.ReadFile(h.path)
		if err != nil {
			return nil, err
		}
		chartdata = data
	}

	// title defaults to the downloaded file's basename. Helm v4's
	// ChartPathOptions.LocateChart downloads any non-local chart (HTTP repo or
	// OCI) into a content-addressed cache and returns a hash-named path (e.g.
	// "<sha256hex>.chart", using the literal extension defined by
	// downloader.CacheChart) rather than a human-readable filename, so
	// filepath.Base(h.path) is meaningless for those sources. In that specific
	// case only, prefer the canonical "<name>-<version>.tgz" form derived from
	// the chart's own Chart.yaml metadata. Genuinely local archives (".tgz" or
	// any other extension a user's file might carry) keep their real filename,
	// even if it doesn't follow the "<name>-<version>.tgz" convention.
	title := filepath.Base(h.path)
	if !info.IsDir() && filepath.Ext(h.path) == ".chart" {
		if ch, err := loader.Load(h.path); err == nil && ch.Metadata != nil && ch.Metadata.Name != "" && ch.Metadata.Version != "" {
			title = fmt.Sprintf("%s-%s.tgz", ch.Metadata.Name, ch.Metadata.Version)
		}
	}

	annotations := make(map[string]string)
	annotations[ocispec.AnnotationTitle] = title

	opener := func() layer.Opener {
		return func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewBuffer(chartdata)), nil
		}
	}
	chartDataLayer, err := layer.FromOpener(opener(),
		layer.WithMediaType(consts.ChartLayerMediaType),
		layer.WithAnnotations(annotations))

	return chartDataLayer, err
}
func isUrl(name string) bool {
	_, err := url.ParseRequestURI(name)
	return err == nil
}

func newRegistryClient(certFile, keyFile, caFile string, insecureSkipTLSverify, plainHTTP bool) (*registry.Client, error) {
	if certFile != "" && keyFile != "" || caFile != "" || insecureSkipTLSverify {
		registryClient, err := newRegistryClientWithTLS(certFile, keyFile, caFile, insecureSkipTLSverify)
		if err != nil {
			return nil, err
		}
		return registryClient, nil
	}
	registryClient, err := newDefaultRegistryClient(plainHTTP)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

func newDefaultRegistryClient(plainHTTP bool) (*registry.Client, error) {
	opts := []registry.ClientOption{
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	}
	if plainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	// create a new registry client
	registryClient, err := registry.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

// newRegistryClientWithTLS builds a registry client backed by an HTTP client with a custom
// TLS config. Helm v4 removed the registry.NewRegistryClientWithTLS convenience wrapper (it
// delegated to helm's internal/tlsutil package, which is not importable outside the helm
// module), so the TLS config construction is inlined here to match its prior behavior.
func newRegistryClientWithTLS(certFile, keyFile, caFile string, insecureSkipTLSverify bool) (*registry.Client, error) {
	tlsConf, err := newTLSConfig(certFile, keyFile, caFile, insecureSkipTLSverify)
	if err != nil {
		return nil, fmt.Errorf("can't create TLS config for client: %w", err)
	}

	registryClient, err := registry.NewClient(
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
		registry.ClientOptHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConf,
				Proxy:           http.ProxyFromEnvironment,
			},
		}),
	)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

// newTLSConfig constructs a *tls.Config from the given cert/key/CA files, mirroring the
// behavior of helm's internal tlsutil.NewTLSConfig.
func newTLSConfig(certFile, keyFile, caFile string, insecureSkipTLSverify bool) (*tls.Config, error) {
	config := &tls.Config{
		InsecureSkipVerify: insecureSkipTLSverify,
	}

	if certFile != "" && keyFile != "" {
		certPEMBlock, err := os.ReadFile(certFile)
		if err != nil {
			return nil, fmt.Errorf("unable to read cert file: %q: %w", certFile, err)
		}
		keyPEMBlock, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("unable to read key file: %q: %w", keyFile, err)
		}
		cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
		if err != nil {
			return nil, fmt.Errorf("unable to load cert from key pair: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	if caFile != "" {
		caPEMBlock, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("can't read CA file: %q: %w", caFile, err)
		}
		cp := x509.NewCertPool()
		if !cp.AppendCertsFromPEM(caPEMBlock) {
			return nil, fmt.Errorf("failed to append certificates from pem block")
		}
		config.RootCAs = cp
	}

	return config, nil
}

// path returns the local filesystem path to the chart archive or directory
func (h *Chart) Path() string {
	return h.path
}
