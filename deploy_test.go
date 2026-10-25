package main

import (
	"bytes"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The image is glibc-based and bakes in its own /usr/lib; a host /usr mounted
// over it left the pod crash-looping on a missing library.
func TestDaemonSetKeepsImageUsr(t *testing.T) {
	b, err := os.ReadFile("deploy/kubernetes/daemonset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var d struct {
			Kind string
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							VolumeMounts []struct {
								MountPath string `yaml:"mountPath"`
							} `yaml:"volumeMounts"`
						}
					}
				}
			}
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			for _, m := range c.VolumeMounts {
				if m.MountPath == "/usr" || m.MountPath == "/lib" {
					t.Errorf("%s hides the image's libraries", m.MountPath)
				}
			}
		}
	}
}

// Describe lists a pod's events through the API; without the rule it printed
// no events and said nothing about why.
func TestDaemonSetRoleReadsEventsAndLogs(t *testing.T) {
	b, err := os.ReadFile("deploy/kubernetes/daemonset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	granted := map[string]bool{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var d struct {
			Kind  string
			Rules []struct {
				Resources []string
				Verbs     []string
			}
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d.Kind != "ClusterRole" {
			continue
		}
		for _, r := range d.Rules {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					granted[res+" "+v] = true
				}
			}
		}
	}
	for _, want := range []string{"pods list", "events list", "pods/log get"} {
		if !granted[want] {
			t.Errorf("ClusterRole lacks %s", want)
		}
	}
}
