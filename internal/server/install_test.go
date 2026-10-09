package server

import (
	"strings"
	"testing"
)

func TestInstallSummary(t *testing.T) {
	tests := []struct {
		name    string
		env     installEnv
		want    []string
		notWant []string
	}{
		{
			name:    "VM: connect via the detected public IP, enable the systemd unit",
			env:     installEnv{},
			want:    []string{"auth-vpn connect 203.0.113.10:7777 --token TOK", "sudo systemctl enable --now auth-vpn", "https://203.0.113.10:9100/ui"},
			notWant: []string{"http://", "kubectl"},
		},
		{
			name:    "Docker: no systemd, still the detected IP",
			env:     installEnv{container: true},
			want:    []string{"auth-vpn connect 203.0.113.10:7777 --token TOK", "https://203.0.113.10:9100/ui"},
			notWant: []string{"systemctl", "Systemd", "http://"},
		},
		{
			name: "Kubernetes: LoadBalancer IP via kubectl, dashboard via port-forward, never the node IP",
			env:  installEnv{container: true, k8sNamespace: "auth-vpn"},
			want: []string{
				"kubectl get svc -n auth-vpn auth-vpn",
				"auth-vpn connect <EXTERNAL-IP>:7777 --token TOK",
				"kubectl port-forward -n auth-vpn deploy/auth-vpn 9100:9100",
				"https://localhost:9100/ui",
			},
			notWant: []string{"203.0.113.10", "systemctl", "Systemd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := installSummary("203.0.113.10", 7777, "TOK", "KEY", tt.env)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("summary missing %q:\n%s", w, got)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("summary should not contain %q:\n%s", nw, got)
				}
			}
		})
	}
}
