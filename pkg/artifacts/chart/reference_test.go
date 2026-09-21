package chart

import (
	"testing"
)

func TestResolvedOCIReference(t *testing.T) {
	chartPath := "../../../testdata/rancher-cluster-templates-0.5.2.tgz"
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{
			name: "adds resolved chart version to repository reference",
			ref:  "oci://ghcr.io/americancode/helm-charts/mariner",
			want: "oci://ghcr.io/americancode/helm-charts/mariner:0.5.2",
		},
		{
			name: "keeps tagged reference",
			ref:  "oci://ghcr.io/americancode/helm-charts/mariner:1.3.0",
			want: "oci://ghcr.io/americancode/helm-charts/mariner:1.3.0",
		},
		{
			name: "keeps digest reference",
			ref:  "oci://ghcr.io/americancode/helm-charts/mariner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			want: "oci://ghcr.io/americancode/helm-charts/mariner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvedOCIReference(tt.ref, chartPath)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolvedOCIReference() = %q, want %q", got, tt.want)
			}
		})
	}
}
