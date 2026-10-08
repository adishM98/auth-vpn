//go:build linux

package client

import (
	"log"
	"os/exec"
)

// applyDNS configures split DNS on the tunnel interface via systemd-resolved:
// lookups for domains go to server, everything else is untouched. The config
// dies with the interface; the returned func reverts it early anyway.
// ponytail: systemd-resolved only; other resolvers get a warning and use IPs.
func applyDNS(ifaceName, server string, domains []string) func() {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		log.Printf("warning: pushed DNS skipped: resolvectl not found (systemd-resolved required)")
		return func() {}
	}
	routing := make([]string, 0, len(domains))
	for _, d := range domains {
		routing = append(routing, "~"+d) // "~" = routing-only domain, not a search suffix
	}
	if out, err := exec.Command("resolvectl", "dns", ifaceName, server).CombinedOutput(); err != nil {
		log.Printf("warning: resolvectl dns: %v: %s", err, out)
		return func() {}
	}
	if out, err := exec.Command("resolvectl", append([]string{"domain", ifaceName}, routing...)...).CombinedOutput(); err != nil {
		log.Printf("warning: resolvectl domain: %v: %s", err, out)
	}
	return func() { _ = exec.Command("resolvectl", "revert", ifaceName).Run() }
}
