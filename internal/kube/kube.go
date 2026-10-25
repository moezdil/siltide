// Package kube maps pod UIDs to pod names, namespaces, and workloads, and
// answers the describe and logs questions the Kubernetes tab asks.
//
// Sources, cheapest first:
//   - the kubelet's log directory, `/var/log/pods/<namespace>_<pod>_<uid>/`,
//     which needs no credentials and also serves container logs;
//   - the API server, in-cluster through the service account or from a
//     kubeconfig, which adds owner references, container names, requests,
//     conditions, and events.
package kube

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// Pod is what siltide shows for a Kubernetes pod.
type Pod struct {
	Name       string
	Namespace  string
	UID        string
	Node       string
	Workload   string            // "Deployment/chat-api"
	Containers map[string]string // container ID: container name
	Phase      string
	Ready      string // "2/2"
	Restarts   int
	Started    time.Time
	Labels     map[string]string
	Requests   map[string]string // resource: quantity summed over containers
	Limits     map[string]string
	Conditions []string // "Ready=True"
}

// Resolver caches pod lookups.
type Resolver struct {
	mu       sync.Mutex
	logDir   string
	pods     map[string]Pod // by UID
	at       time.Time
	ttl      time.Duration
	client   *http.Client
	apiURL   string
	token    string
	node     string
	enabled  bool
	source   string // "log-dir", "in-cluster", "kubeconfig"
	attempts []Attempt
	nodes    []NodeInfo
	nodesAt  time.Time
}

// Attempt is one place siltide looked for Kubernetes and what came of it.
// A machine with no cluster on it is the normal case, so the list is how
// someone who expected one finds out which assumption was wrong.
type Attempt struct {
	What  string // "pod log directory", "in-cluster service account", "kubeconfig"
	Where string // the path or address that was tried
	Err   string // why it did not work; empty when it did
}

// Attempts lists the sources that were tried, in the order they were tried.
func (r *Resolver) Attempts() []Attempt { return append([]Attempt(nil), r.attempts...) }

func (r *Resolver) tried(what, where string, err error) {
	a := Attempt{What: what, Where: where}
	if err != nil {
		a.Err = err.Error()
	}
	r.attempts = append(r.attempts, a)
}

// Options select how the API is reached.
type Options struct {
	Kubeconfig string // "" means in-cluster, then `~/.kube/config`
	Context    string
}

// New returns a resolver. It is inert on machines without Kubernetes.
func New(o Options) *Resolver {
	logDir := "/var/log/pods"
	if d := os.Getenv("SILTIDE_POD_LOG_DIR"); d != "" {
		logDir = d // for a runtime that keeps pod logs somewhere else
	}
	r := &Resolver{logDir: logDir, pods: map[string]Pod{}, ttl: 30 * time.Second}
	_, err := os.Stat(r.logDir)
	r.tried("pod log directory", r.logDir, err)
	if err == nil {
		r.enabled, r.source = true, "log-dir"
	}
	r.node = os.Getenv("NODE_NAME")
	if r.node == "" {
		r.node, _ = os.Hostname()
	}
	if !r.inCluster() {
		path := o.Kubeconfig
		if path == "" {
			path = os.Getenv("KUBECONFIG")
		}
		if path == "" {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, ".kube", "config")
		}
		r.tried("kubeconfig", path, r.fromKubeconfig(path, o.Context))
	}
	return r
}

// Enabled reports whether any pod source exists.
func (r *Resolver) Enabled() bool { return r.enabled }

// Source names the pod source in use.
func (r *Resolver) Source() string { return r.source }

func (r *Resolver) inCluster() bool {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount"
	tok, err := os.ReadFile(sa + "/token")
	switch {
	case host == "":
		r.tried("in-cluster service account", sa, errors.New("KUBERNETES_SERVICE_HOST is not set: not running in a pod"))
		return false
	case err != nil:
		r.tried("in-cluster service account", sa+"/token", err)
		return false
	}
	r.tried("in-cluster service account", "https://"+host+":"+port, nil)
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(sa + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	r.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	r.apiURL = "https://" + host + ":" + port
	r.token = strings.TrimSpace(string(tok))
	r.enabled, r.source = true, "in-cluster"
	return true
}

// kubeconfig is the subset siltide reads: token or client certificates.
type kubeconfig struct {
	Current  string `yaml:"current-context"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server   string `yaml:"server"`
			CA       string `yaml:"certificate-authority"`
			CAData   string `yaml:"certificate-authority-data"`
			Insecure bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token        string `yaml:"token"`
			TokenFile    string `yaml:"tokenFile"`
			Cert         string `yaml:"client-certificate"`
			CertData     string `yaml:"client-certificate-data"`
			Key          string `yaml:"client-key"`
			KeyData      string `yaml:"client-key-data"`
			Username     string `yaml:"username"`
			Password     string `yaml:"password"`
			Exec         any    `yaml:"exec"`
			AuthProvider any    `yaml:"auth-provider"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// fromKubeconfig configures the API client from a kubeconfig file.
// ponytail: exec credential plugins (cloud CLIs) are not run; those
// clusters get log-dir data only.
func (r *Resolver) fromKubeconfig(path, context string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kc kubeconfig
	if err := yaml.Unmarshal(b, &kc); err != nil {
		return err
	}
	if context == "" {
		context = kc.Current
	}
	var clusterName, userName string
	for _, c := range kc.Contexts {
		if c.Name == context {
			clusterName, userName = c.Context.Cluster, c.Context.User
		}
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	server := ""
	for _, c := range kc.Clusters {
		if c.Name != clusterName {
			continue
		}
		server = c.Cluster.Server
		if pem := fileOrData(path, c.Cluster.CA, c.Cluster.CAData); pem != nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			tlsCfg.RootCAs = pool
		}
		tlsCfg.InsecureSkipVerify = c.Cluster.Insecure // #nosec G402 -- mirrors the user's kubeconfig
	}
	if server == "" {
		return errors.New("kubeconfig: no cluster for context " + context)
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		switch {
		case u.User.Token != "":
			r.token = u.User.Token
		case u.User.TokenFile != "":
			if t, err := os.ReadFile(u.User.TokenFile); err == nil {
				r.token = strings.TrimSpace(string(t))
			}
		}
		cert, key := fileOrData(path, u.User.Cert, u.User.CertData), fileOrData(path, u.User.Key, u.User.KeyData)
		if cert != nil && key != nil {
			if pair, err := tls.X509KeyPair(cert, key); err == nil {
				tlsCfg.Certificates = []tls.Certificate{pair}
			}
		}
		if r.token == "" && len(tlsCfg.Certificates) == 0 {
			return errors.New("kubeconfig: user " + userName + " needs a token or client certificate (exec plugins are not supported)")
		}
	}
	r.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	r.apiURL = strings.TrimRight(server, "/")
	r.enabled, r.source = true, "kubeconfig"
	return nil
}

func fileOrData(kubeconfigPath, file, data string) []byte {
	if data != "" {
		if b, err := base64.StdEncoding.DecodeString(data); err == nil {
			return b
		}
	}
	if file == "" {
		return nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(filepath.Dir(kubeconfigPath), file)
	}
	b, _ := os.ReadFile(file)
	return b
}

// Lookup returns the pod for a UID.
func (r *Resolver) Lookup(uid string) (Pod, bool) {
	if uid == "" || !r.enabled {
		return Pod{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.at) > r.ttl {
		r.refresh()
	}
	p, ok := r.pods[uid]
	return p, ok
}

// Pods lists every known pod.
func (r *Resolver) Pods() []Pod {
	if !r.enabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.at) > r.ttl {
		r.refresh()
	}
	out := make([]Pod, 0, len(r.pods))
	for _, p := range r.pods {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+out[j].Name < out[j].Namespace+out[j].Name })
	return out
}

func (r *Resolver) refresh() {
	r.at = time.Now()
	pods := map[string]Pod{}
	entries, _ := os.ReadDir(r.logDir)
	for _, e := range entries {
		if p, ok := ParseLogDir(e.Name()); ok {
			pods[p.UID] = p
		}
	}
	if r.client != nil {
		for _, p := range r.fromAPI() {
			pods[p.UID] = p
		}
	}
	r.pods = pods
}

// ParseLogDir reads "<namespace>_<pod>_<uid>".
func ParseLogDir(name string) (Pod, bool) {
	parts := strings.Split(name, "_")
	if len(parts) != 3 || len(parts[2]) != 36 {
		return Pod{}, false
	}
	return Pod{Namespace: parts[0], Name: parts[1], UID: parts[2], Workload: guessWorkload(parts[1])}, true
}

// guessWorkload strips the hashes Kubernetes appends to pod names.
// "chat-api-7d9f8b6c5-x2k9p" is a Deployment named chat-api; "web-0" is a
// StatefulSet named web.
func guessWorkload(pod string) string {
	parts := strings.Split(pod, "-")
	switch {
	case len(parts) >= 3 && isHash(parts[len(parts)-1], 5) && isHash(parts[len(parts)-2], 8):
		return "Deployment/" + strings.Join(parts[:len(parts)-2], "-")
	case len(parts) >= 2 && isHash(parts[len(parts)-1], 5):
		return "ReplicaSet/" + strings.Join(parts[:len(parts)-1], "-")
	case len(parts) >= 2 && isDigits(parts[len(parts)-1]):
		return "StatefulSet/" + strings.Join(parts[:len(parts)-1], "-")
	}
	return "Pod/" + pod
}

func isHash(s string, n int) bool {
	if len(s) != n && (n != 8 || len(s) < 8 || len(s) > 10) {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdfghjklmnpqrstvwxz", c) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type podJSON struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		UID       string            `json:"uid"`
		Labels    map[string]string `json:"labels"`
		Created   time.Time         `json:"creationTimestamp"`
		Owners    []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name      string `json:"name"`
			Resources struct {
				Requests map[string]string `json:"requests"`
				Limits   map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		Containers []struct {
			Name     string `json:"name"`
			ID       string `json:"containerID"` // "containerd://<id>"
			Ready    bool   `json:"ready"`
			Restarts int    `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (r *Resolver) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiURL+path, nil)
	if err != nil {
		return err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	if w, ok := out.(io.Writer); ok {
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *Resolver) fromAPI() []Pod {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list struct {
		Items []podJSON `json:"items"`
	}
	path := "/api/v1/pods?fieldSelector=spec.nodeName=" + url.QueryEscape(r.node)
	if r.source == "kubeconfig" {
		path = "/api/v1/pods?limit=2000" // every node: this siltide may watch a fleet
	}
	if r.get(ctx, path, &list) != nil {
		return nil
	}
	out := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, convert(it))
	}
	return out
}

func convert(it podJSON) Pod {
	p := Pod{Name: it.Metadata.Name, Namespace: it.Metadata.Namespace, UID: it.Metadata.UID, Node: it.Spec.NodeName,
		Containers: map[string]string{}, Phase: it.Status.Phase, Started: it.Metadata.Created, Labels: it.Metadata.Labels,
		Requests: map[string]string{}, Limits: map[string]string{}}
	for _, o := range it.Metadata.Owners {
		p.Workload = o.Kind + "/" + o.Name
		if o.Kind == "ReplicaSet" {
			if i := strings.LastIndex(o.Name, "-"); i > 0 {
				p.Workload = "Deployment/" + o.Name[:i]
			}
		}
	}
	if p.Workload == "" {
		p.Workload = guessWorkload(p.Name)
	}
	ready := 0
	for _, c := range it.Status.Containers {
		id := c.ID[strings.LastIndex(c.ID, "/")+1:]
		p.Containers[id] = c.Name
		p.Restarts += c.Restarts
		if c.Ready {
			ready++
		}
	}
	p.Ready = fmt.Sprintf("%d/%d", ready, len(it.Spec.Containers))
	for _, c := range it.Spec.Containers {
		for k, v := range c.Resources.Requests {
			p.Requests[k] = join(p.Requests[k], v)
		}
		for k, v := range c.Resources.Limits {
			p.Limits[k] = join(p.Limits[k], v)
		}
	}
	for _, c := range it.Status.Conditions {
		p.Conditions = append(p.Conditions, c.Type+"="+c.Status)
	}
	return p
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "+" + b
}

// ContainerName resolves a container ID within a pod, or a short ID.
func (p Pod) ContainerName(id string) string {
	if n, ok := p.Containers[id]; ok {
		return n
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Describe returns a `kubectl describe` style text for a pod, including its
// recent events when the API is reachable.
func (r *Resolver) Describe(ctx context.Context, ns, name string) (string, error) {
	var p Pod
	found := false
	for _, q := range r.Pods() {
		if q.Namespace == ns && q.Name == name {
			p, found = q, true
		}
	}
	if !found {
		return "", fmt.Errorf("pod %s/%s not known", ns, name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Name:       %s\nNamespace:  %s\nNode:       %s\nWorkload:   %s\nUID:        %s\n", p.Name, p.Namespace, orDash(p.Node), p.Workload, p.UID)
	if p.Phase != "" {
		fmt.Fprintf(&b, "Status:     %s (ready %s, restarts %d, age %s)\n", p.Phase, p.Ready, p.Restarts, age(p.Started))
	}
	if len(p.Labels) > 0 {
		b.WriteString("Labels:     " + kvList(p.Labels) + "\n")
	}
	if len(p.Requests) > 0 || len(p.Limits) > 0 {
		fmt.Fprintf(&b, "Requests:   %s\nLimits:     %s\n", kvList(p.Requests), kvList(p.Limits))
	}
	if len(p.Containers) > 0 {
		var names []string
		for _, n := range p.Containers {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("Containers: " + strings.Join(names, ", ") + "\n")
	}
	if len(p.Conditions) > 0 {
		b.WriteString("Conditions: " + strings.Join(p.Conditions, ", ") + "\n")
	}
	if r.client != nil {
		var evs struct {
			Items []struct {
				Type    string    `json:"type"`
				Reason  string    `json:"reason"`
				Message string    `json:"message"`
				Last    time.Time `json:"lastTimestamp"`
				Event   time.Time `json:"eventTime"`
				First   time.Time `json:"firstTimestamp"`
			} `json:"items"`
		}
		sel := "involvedObject.name=" + name + ",involvedObject.namespace=" + ns
		if p.UID != "" {
			sel += ",involvedObject.uid=" + p.UID // a recreated pod keeps its name, not its events
		}
		q := url.QueryEscape(sel)
		if r.get(ctx, "/api/v1/namespaces/"+ns+"/events?fieldSelector="+q, &evs) == nil && len(evs.Items) > 0 {
			b.WriteString("\nEvents:\n")
			for _, e := range evs.Items {
				at := e.Last
				for _, t := range []time.Time{e.Event, e.First} {
					if at.IsZero() {
						at = t
					}
				}
				fmt.Fprintf(&b, "  %s %-8s %-20s %s\n", at.Format("15:04:05"), e.Type, e.Reason, e.Message)
			}
		}
	}
	return b.String(), nil
}

// Logs returns the last n lines of a container: from the API when it is
// reachable, otherwise straight from the kubelet's log files on the node.
func (r *Resolver) Logs(ctx context.Context, ns, name, container string, n int) (string, error) {
	if r.client != nil {
		var b strings.Builder
		path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d", ns, name, n)
		if container != "" {
			path += "&container=" + url.QueryEscape(container)
		}
		if err := r.get(ctx, path, &b); err == nil {
			return b.String(), nil
		}
	}
	for _, p := range r.Pods() {
		if p.Namespace != ns || p.Name != name {
			continue
		}
		dir := filepath.Join(r.logDir, ns+"_"+name+"_"+p.UID)
		if container == "" {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() {
					container = e.Name()
					break
				}
			}
		}
		files, _ := filepath.Glob(filepath.Join(dir, container, "*.log"))
		if len(files) == 0 {
			return "", fmt.Errorf("no log files under %s", dir)
		}
		sort.Strings(files)
		return tail(files[len(files)-1], n)
	}
	return "", fmt.Errorf("pod %s/%s not known", ns, name)
}

// tail returns the last n lines of a CRI (Container Runtime Interface) log file, stripping the
// "<time> <stream> <flag> " prefix the runtime adds.
func tail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if f := strings.SplitN(line, " ", 4); len(f) == 4 {
			line = f[3]
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, line)
	}
	return strings.Join(ring, "\n"), nil
}

func kvList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func age(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d > 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d > time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
