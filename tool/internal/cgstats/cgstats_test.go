package cgstats

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectAndWrite(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("ab", 32)
	dir := filepath.Join(root, "system.slice", "docker-"+id+".scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": "1000000\n",
		"memory.stat":    "anon 400000\ninactive_file 250000\nactive_file 10\n",
		"memory.max":     "134217728\n",
		"cpu.stat":       "usage_usec 2500000\nuser_usec 1\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix socket unavailable:", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": id, "Names": []string{"/app_go"}, "Image": "app_go:abc1234",
				"Labels": map[string]string{"com.docker.compose.project": "app_go", "com.docker.compose.service": "app"}},
			{"Id": strings.Repeat("cd", 32), "Names": []string{"/no_cgroup"}, "Image": "x"},
		})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	c := NewCollector(sock, root)
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples: %d", len(samples))
	}
	var b bytes.Buffer
	Write(&b, samples)
	out := b.String()
	for _, want := range []string{
		`container_memory_working_set_bytes{id="/docker/` + id + `",name="app_go",image="app_go:abc1234",compose_project="app_go",compose_service="app"} 750000`,
		`container_spec_memory_limit_bytes{id="/docker/` + id + `",name="app_go"`,
		`container_cpu_usage_seconds_total{id="/docker/` + id + `",name="app_go",image="app_go:abc1234",compose_project="app_go",compose_service="app"} 2.5`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, `name="no_cgroup"`) {
		t.Fatal("container without cgroup must be skipped")
	}
}
