package docker

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func TestDiscoverContainerdAndRejectClassicStore(t *testing.T) {
	for _, tc := range []struct {
		name, info, want string
		wantError        bool
	}{
		{"containerd", `{"DriverStatus":[["driver-type","io.containerd.snapshotter.v1"]],"Containerd":{"Address":"/custom/containerd.sock","Namespaces":{"Containers":"custom"}}}`, "/custom/containerd.sock", false},
		{"classic", `{"Driver":"overlay2","DriverStatus":[["Backing Filesystem","extfs"]]}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "docker.sock")
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/info" {
					t.Errorf("unexpected API request: %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, tc.info)
			})}
			t.Cleanup(func() { _ = server.Close() })
			go func() { _ = server.Serve(listener) }()
			address, namespace, err := discover(t.Context(), path)
			if (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if address != tc.want {
				t.Fatalf("address: %s", address)
			}
			if !tc.wantError && namespace != "custom" {
				t.Fatalf("namespace: %s", namespace)
			}
		})
	}
}
