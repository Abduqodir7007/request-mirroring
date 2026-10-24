package cgstats

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Container struct {
	ID     string
	Name   string
	Image  string
	Labels map[string]string
}

type Sample struct {
	Container
	WorkingSet  uint64
	Usage       uint64
	Limit       uint64
	CPUSeconds  float64
	HasLimit    bool
	CgroupFound bool
}

type Collector struct {
	CgroupRoot string
	Docker     *http.Client
	Scopes     []string
}

func NewCollector(socket, cgroupRoot string) *Collector {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	return &Collector{
		CgroupRoot: cgroupRoot,
		Docker:     &http.Client{Transport: tr, Timeout: 5 * time.Second},
		Scopes:     []string{"system.slice/docker-%s.scope", "docker/%s"},
	}
}

func (c *Collector) containers(ctx context.Context) ([]Container, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Docker.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("docker: %d %s", resp.StatusCode, b)
	}
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := r.ID[:12]
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		out = append(out, Container{ID: r.ID, Name: name, Image: r.Image, Labels: r.Labels})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Collector) Collect(ctx context.Context) ([]Sample, error) {
	list, err := c.containers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(list))
	for _, ct := range list {
		s := Sample{Container: ct}
		if dir := c.cgroupDir(ct.ID); dir != "" {
			s.CgroupFound = true
			s.Usage = readUint(filepath.Join(dir, "memory.current"))
			inactive := statValue(filepath.Join(dir, "memory.stat"), "inactive_file")
			if s.Usage > inactive {
				s.WorkingSet = s.Usage - inactive
			}
			if lim, ok := readLimit(filepath.Join(dir, "memory.max")); ok {
				s.Limit, s.HasLimit = lim, true
			}
			s.CPUSeconds = float64(statValue(filepath.Join(dir, "cpu.stat"), "usage_usec")) / 1e6
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *Collector) cgroupDir(id string) string {
	for _, pattern := range c.Scopes {
		dir := filepath.Join(c.CgroupRoot, fmt.Sprintf(pattern, id))
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return ""
}

func readUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func readLimit(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" || s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func statValue(path, key string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == key {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

func Write(w io.Writer, samples []Sample) {
	type metric struct {
		name, help, kind string
		value            func(Sample) (float64, bool)
	}
	ms := []metric{
		{"container_memory_working_set_bytes", "Current working set of the container in bytes", "gauge", func(s Sample) (float64, bool) { return float64(s.WorkingSet), s.CgroupFound }},
		{"container_memory_usage_bytes", "Current memory usage in bytes, including all memory regardless of when it was accessed", "gauge", func(s Sample) (float64, bool) { return float64(s.Usage), s.CgroupFound }},
		{"container_spec_memory_limit_bytes", "Memory limit for the container", "gauge", func(s Sample) (float64, bool) { return float64(s.Limit), s.CgroupFound }},
		{"container_cpu_usage_seconds_total", "Cumulative cpu time consumed in seconds", "counter", func(s Sample) (float64, bool) { return s.CPUSeconds, s.CgroupFound }},
	}
	for _, m := range ms {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
		for _, s := range samples {
			v, ok := m.value(s)
			if !ok {
				continue
			}
			fmt.Fprintf(w, "%s{id=%q,name=%q,image=%q,compose_project=%q,compose_service=%q} %s\n", m.name,
				"/docker/"+s.ID, s.Name, s.Image, s.Labels["com.docker.compose.project"], s.Labels["com.docker.compose.service"],
				strconv.FormatFloat(v, 'g', -1, 64))
		}
	}
}
