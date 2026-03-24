package k8s

import (
	"testing"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRuntimeConfig(t *testing.T) {
	t.Run("parse namespaces", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want []string
		}{
			{"empty string", "", nil},
			{"single namespace", "default", []string{"default"}},
			{"multiple namespaces", "ns1,ns2,ns3", []string{"ns1", "ns2", "ns3"}},
			{"with whitespace", " ns1 , ns2 , ns3 ", []string{"ns1", "ns2", "ns3"}},
			{"trailing comma", "ns1,", []string{"ns1"}},
			{"only commas", ",,,", nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := ParseNamespaces(tt.raw)
				if len(got) != len(tt.want) {
					t.Fatalf("ParseNamespaces(%q) = %v, want %v", tt.raw, got, tt.want)
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("ParseNamespaces(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
					}
				}
			})
		}
	})

	t.Run("all namespaces defaults when namespace list is empty", func(t *testing.T) {
		cfg, err := (RuntimeConfig{ListenAddr: ":8080"}).Normalize()
		if err != nil {
			t.Fatalf("Normalize() error: %v", err)
		}
		if !cfg.AllNamespaces() {
			t.Fatal("expected AllNamespaces() to be true with empty namespace list")
		}
		if got, want := cfg.ScopeDescription(), "all namespaces"; got != want {
			t.Fatalf("ScopeDescription() = %q, want %q", got, want)
		}
	})

	t.Run("explicit namespaces are normalized and scoped", func(t *testing.T) {
		cfg, err := (RuntimeConfig{
			ListenAddr: ":8080",
			Namespaces: []string{" default ", "kube-system"},
		}).Normalize()
		if err != nil {
			t.Fatalf("Normalize() error: %v", err)
		}
		if cfg.AllNamespaces() {
			t.Fatal("expected AllNamespaces() to be false with explicit namespaces")
		}
		if len(cfg.Namespaces) != 2 || cfg.Namespaces[0] != "default" || cfg.Namespaces[1] != "kube-system" {
			t.Fatalf("Namespaces = %v, want [default kube-system]", cfg.Namespaces)
		}
		if got, want := cfg.ScopeDescription(), "default,kube-system"; got != want {
			t.Fatalf("ScopeDescription() = %q, want %q", got, want)
		}
	})

	t.Run("normalize rejects invalid config", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  RuntimeConfig
		}{
			{
				name: "missing listen address",
				cfg:  RuntimeConfig{},
			},
			{
				name: "duplicate namespaces",
				cfg: RuntimeConfig{
					ListenAddr: ":8080",
					Namespaces: []string{"default", "default"},
				},
			},
			{
				name: "blank namespace value",
				cfg: RuntimeConfig{
					ListenAddr: ":8080",
					Namespaces: []string{"default", " "},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := tt.cfg.Normalize(); err == nil {
					t.Fatal("expected Normalize() to fail")
				}
			})
		}
	})

	t.Run("build cache options honors namespace scope", func(t *testing.T) {
		t.Run("all namespaces uses zero-value cache options", func(t *testing.T) {
			opts, err := BuildCacheOptions(RuntimeConfig{ListenAddr: ":8080"})
			if err != nil {
				t.Fatalf("BuildCacheOptions() error: %v", err)
			}
			if len(opts.DefaultNamespaces) != 0 {
				t.Fatalf("DefaultNamespaces = %v, want empty", opts.DefaultNamespaces)
			}
		})

		t.Run("explicit namespaces populate DefaultNamespaces", func(t *testing.T) {
			opts, err := BuildCacheOptions(RuntimeConfig{
				ListenAddr: ":8080",
				Namespaces: []string{"team-a", "team-b"},
			})
			if err != nil {
				t.Fatalf("BuildCacheOptions() error: %v", err)
			}
			if len(opts.DefaultNamespaces) != 2 {
				t.Fatalf("DefaultNamespaces length = %d, want 2", len(opts.DefaultNamespaces))
			}
			if _, ok := opts.DefaultNamespaces["team-a"]; !ok {
				t.Fatal("expected team-a in DefaultNamespaces")
			}
			if _, ok := opts.DefaultNamespaces["team-b"]; !ok {
				t.Fatal("expected team-b in DefaultNamespaces")
			}
		})
	})

	t.Run("scheme registers Kubernetes and CloudNativePG types", func(t *testing.T) {
		scheme, err := NewScheme()
		if err != nil {
			t.Fatalf("NewScheme() error: %v", err)
		}

		if !scheme.IsGroupRegistered("") {
			t.Fatal("expected core API group to be registered")
		}

		for _, kind := range []string{"Cluster", "Backup", "ScheduledBackup"} {
			gvk := schema.GroupVersionKind{
				Group:   cnpgv1.SchemeGroupVersion.Group,
				Version: cnpgv1.SchemeGroupVersion.Version,
				Kind:    kind,
			}
			if !scheme.Recognizes(gvk) {
				t.Fatalf("expected scheme to recognize %v", gvk)
			}
		}
	})

	t.Run("rest config surfaces explicit failure cases", func(t *testing.T) {
		t.Run("requires in-cluster config when kubeconfig is empty", func(t *testing.T) {
			_, err := BuildRESTConfig(RuntimeConfig{ListenAddr: ":8080"})
			if err == nil {
				t.Fatal("expected error when neither kubeconfig nor in-cluster env is set")
			}
		})

		t.Run("returns an error for an invalid kubeconfig path", func(t *testing.T) {
			_, err := BuildRESTConfig(RuntimeConfig{
				ListenAddr: ":8080",
				Kubeconfig: "/nonexistent/path/kubeconfig",
			})
			if err == nil {
				t.Fatal("expected error for nonexistent kubeconfig path")
			}
		})
	})
}
