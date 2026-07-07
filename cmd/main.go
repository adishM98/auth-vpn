package main

import (
	"bytes"
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/client"
	"github.com/adishM98/auth-vpn/internal/hub"
	"github.com/adishM98/auth-vpn/internal/server"
	"github.com/adishM98/auth-vpn/internal/updater"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// ─── root ────────────────────────────────────────────────────────────────────

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "auth-vpn",
		Short: "Lightweight self-hosted VPN tunnel for developers and CI/CD",
	}
	root.AddCommand(serverCmd(), connectCmd(), disconnectCmd(), statusCmd(), profileCmd(), versionCmd(), updateCmd(), hubCmd())
	return root
}

// ─── server ──────────────────────────────────────────────────────────────────

func serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage the auth-vpn server",
	}
	cmd.AddCommand(serverInstallCmd(), serverStartCmd(), serverTokensCmd(), serverClientsCmd(), serverChangePortCmd())
	return cmd
}

func serverInstallCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and configure the auth-vpn server (run once on VM)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Installing auth-vpn server...")
			fmt.Print("  ✓ Detecting public IP... ")

			publicIP, rawToken, apiKey, err := server.Install(port)
			if err != nil {
				return err
			}

			fmt.Println(publicIP)
			fmt.Println("  ✓ TLS certificate generated")
			fmt.Println("  ✓ Initial token created")
			fmt.Println("  ✓ Server config written to", server.ServerConfigFile)
			fmt.Println("  ✓ ACL config written to", server.ACLFile)
			fmt.Println("  ✓ Systemd service written")
			fmt.Println()
			fmt.Println("  Run:  sudo systemctl enable --now auth-vpn")
			fmt.Println()
			fmt.Println("  ─────────────────────────────────────────────")
			fmt.Printf("  Connect with:\n")
			fmt.Printf("    auth-vpn connect %s:%d --token %s\n", publicIP, port, rawToken)
			fmt.Println()
			fmt.Printf("  Web dashboard:  http://%s:9100/ui\n", publicIP)
			fmt.Printf("  API key:        %s\n", apiKey)
			fmt.Println("  ─────────────────────────────────────────────")
			return nil
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 7777, "Port to listen on")
	return cmd
}

func serverChangePortCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "change-port",
		Short: "Change the tunnel listen port and restart the service",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := server.LoadServerConfig(server.ServerConfigFile)
			if err != nil {
				return fmt.Errorf("load server config: %w", err)
			}
			currentPort := cfg.Port
			if currentPort == 0 {
				currentPort = 7777
			}

			fmt.Printf("\n  Current tunnel port: %d\n", currentPort)
			fmt.Printf("  Enter new port [%d]: ", currentPort)

			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			input := strings.TrimSpace(scanner.Text())

			newPort := currentPort
			if input != "" {
				p, err := strconv.Atoi(input)
				if err != nil || p < 1 || p > 65535 {
					return fmt.Errorf("invalid port %q: must be a number between 1 and 65535", input)
				}
				newPort = p
			}

			if newPort == currentPort {
				fmt.Printf("  Port unchanged (%d)\n\n", currentPort)
				return nil
			}

			cfg.Port = newPort
			if err := server.SaveServerConfig(server.ServerConfigFile, cfg); err != nil {
				return fmt.Errorf("save server config: %w", err)
			}
			fmt.Printf("  ✓ server.yaml updated: port → %d\n", newPort)

			if err := server.WriteSystemdService(newPort); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: update systemd service: %v\n", err)
			} else {
				fmt.Println("  ✓ systemd service updated")
			}

			if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: daemon-reload: %v %s\n", err, out)
			}
			if out, err := exec.Command("systemctl", "restart", "auth-vpn").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: restart auth-vpn: %v %s\n", err, out)
				fmt.Println("  Run manually: sudo systemctl daemon-reload && sudo systemctl restart auth-vpn")
			} else {
				fmt.Println("  ✓ auth-vpn restarted")
			}

			fmt.Println()
			fmt.Println("  ─────────────────────────────────────────────")
			fmt.Printf("  ⚠  Next steps:\n")
			fmt.Printf("     1. Open port %d in your firewall / NSG\n", newPort)
			fmt.Printf("     2. Close port %d in your firewall / NSG\n", currentPort)
			fmt.Printf("     3. Update DATASOURCE_VPN_HOST to <ip>:%d\n", newPort)
			fmt.Println("  ─────────────────────────────────────────────")
			fmt.Println()
			return nil
		},
	}
}

func serverStartCmd() *cobra.Command {
	var port int
	var subnet, serverIP, metricsAddr, aclPath, apiKey, forwardBind, sshAddr string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the auth-vpn server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load server.yaml if it exists; flags override file values.
			cfg := server.Config{
				Port:       port,
				TLSCert:    server.CertFile,
				TLSKey:     server.KeyFile,
				TokensPath: server.TokensFile,
			}
			if sc, err := server.LoadServerConfig(server.ServerConfigFile); err == nil {
				if !cmd.Flags().Changed("port") && sc.Port != 0 {
					cfg.Port = sc.Port
				}
				if !cmd.Flags().Changed("subnet") {
					cfg.Subnet = sc.Subnet
				}
				if !cmd.Flags().Changed("server-ip") {
					cfg.ServerIP = sc.ServerIP
				}
				if !cmd.Flags().Changed("metrics-addr") {
					cfg.MetricsAddr = sc.MetricsAddr
				}
				if !cmd.Flags().Changed("acl") {
					cfg.ACLPath = sc.ACLPath
				}
				if !cmd.Flags().Changed("api-key") {
					cfg.APIKey = sc.APIKey
				}
				if !cmd.Flags().Changed("forward-bind") {
					cfg.ForwardBindAddr = sc.ForwardBindAddr
				}
				if !cmd.Flags().Changed("ssh-addr") {
					cfg.SSHAddr = sc.SSHAddr
				}
			}
			// Apply explicit flag overrides.
			if cmd.Flags().Changed("subnet") {
				cfg.Subnet = subnet
			}
			if cmd.Flags().Changed("server-ip") {
				cfg.ServerIP = serverIP
			}
			if cmd.Flags().Changed("metrics-addr") {
				cfg.MetricsAddr = metricsAddr
			}
			if cmd.Flags().Changed("acl") {
				cfg.ACLPath = aclPath
			}
			if cmd.Flags().Changed("api-key") {
				cfg.APIKey = apiKey
			}
			if cmd.Flags().Changed("forward-bind") {
				cfg.ForwardBindAddr = forwardBind
			}
			if cmd.Flags().Changed("ssh-addr") {
				cfg.SSHAddr = sshAddr
			}

			srv, err := server.New(&cfg)
			if err != nil {
				return err
			}
			return srv.Start()
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 7777, "Port to listen on")
	cmd.Flags().StringVar(&subnet, "subnet", "", "VPN subnet CIDR (default 10.0.0.0/24)")
	cmd.Flags().StringVar(&serverIP, "server-ip", "", "Server TUN IP (default 10.0.0.1)")
	cmd.Flags().StringVar(&metricsAddr, "metrics-addr", "", "Metrics/API listen address (default localhost:9100)")
	cmd.Flags().StringVar(&aclPath, "acl", "", "Path to acl.yaml (empty = allow all)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Bearer key for /api/* and /plugin/* (empty = no auth)")
	cmd.Flags().StringVar(&forwardBind, "forward-bind", "", "IP to bind direct-forward listeners (e.g. 172.190.141.231); empty = all interfaces")
	cmd.Flags().StringVar(&sshAddr, "ssh-addr", "", "Embedded SSH server address (e.g. :2222); empty = disabled")
	return cmd
}

// ─── server tokens ───────────────────────────────────────────────────────────

func serverTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage access tokens",
	}
	cmd.AddCommand(tokensListCmd(), tokensAddCmd(), tokensRevokeCmd())
	return cmd
}

func tokenManager() (*auth.Manager, error) {
	return auth.NewManager(server.TokensFile)
}

func tokensListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			tm, err := tokenManager()
			if err != nil {
				return err
			}
			tokens := tm.List()
			if len(tokens) == 0 {
				fmt.Println("no tokens")
				return nil
			}
			fmt.Printf("%-20s  %-25s  %s\n", "NAME", "CREATED", "EXPIRES")
			for _, t := range tokens {
				exp := "never"
				if t.ExpiresAt != nil {
					exp = t.ExpiresAt.Format(time.DateTime)
				}
				fmt.Printf("%-20s  %-25s  %s\n",
					t.Name, t.CreatedAt.Format(time.DateTime), exp)
			}
			return nil
		},
	}
}

func tokensAddCmd() *cobra.Command {
	var name, expires string
	var oneTime bool
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a new token",
		RunE: func(cmd *cobra.Command, args []string) error {
			tm, err := tokenManager()
			if err != nil {
				return err
			}

			var expiresAt *time.Time
			if expires != "" {
				d, err := time.ParseDuration(expires)
				if err != nil {
					return fmt.Errorf("invalid --expires %q (use Go duration e.g. 24h, 7d not supported — use 168h): %w", expires, err)
				}
				t := time.Now().Add(d)
				expiresAt = &t
			}

			raw, err := tm.Add(name, expiresAt, oneTime)
			if err != nil {
				return err
			}

			fmt.Printf("\nToken created for %q\n\n", name)
			fmt.Printf("  auth-vpn connect <host>:7777 --token %s\n\n", raw)
			if oneTime {
				fmt.Println("  (one-time use — expires after first connection)")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "Descriptive name for this token (required)")
	cmd.Flags().StringVar(&expires, "expires", "", "Expiry duration, e.g. 24h, 720h")
	cmd.Flags().BoolVar(&oneTime, "one-time", false, "Revoke after first successful use")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func tokensRevokeCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a token by name",
		RunE: func(cmd *cobra.Command, args []string) error {
			tm, err := tokenManager()
			if err != nil {
				return err
			}
			if err := tm.Revoke(name); err != nil {
				return err
			}
			fmt.Printf("token %q revoked\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "Token name to revoke (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// ─── server clients ──────────────────────────────────────────────────────────

func serverClientsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clients",
		Short: "Show currently connected clients (run on the server)",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := net.Dial("unix", server.SocketFile)
			if err != nil {
				return fmt.Errorf("cannot connect to server socket (%s): is the server running?\n%w",
					server.SocketFile, err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

			if err := json.NewEncoder(conn).Encode(map[string]string{"cmd": "clients"}); err != nil {
				return err
			}

			var resp struct {
				Clients []struct {
					Name        string    `json:"name"`
					IP          string    `json:"ip"`
					ConnectedAt time.Time `json:"connected_at"`
				} `json:"clients"`
				Error string `json:"error,omitempty"`
			}
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				return err
			}
			if resp.Error != "" {
				return fmt.Errorf("server error: %s", resp.Error)
			}
			if len(resp.Clients) == 0 {
				fmt.Println("no clients connected")
				return nil
			}
			fmt.Printf("%-20s  %-15s  %s\n", "NAME", "TUNNEL IP", "CONNECTED AT")
			for _, c := range resp.Clients {
				fmt.Printf("%-20s  %-15s  %s\n",
					c.Name, c.IP, c.ConnectedAt.Local().Format(time.DateTime))
			}
			return nil
		},
	}
}

// ─── connect ─────────────────────────────────────────────────────────────────

func connectCmd() *cobra.Command {
	var token, apiKey, apiURL string
	var apiPort int
	var background, wait, insecure, reconnect, githubAction bool
	var forwardRules, extraRoutes []string

	cmd := &cobra.Command{
		Use:   "connect <host:port|profile-name>",
		Short: "Connect to an auth-vpn server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			opts := client.Options{
				Token:       token,
				Background:  background,
				Wait:        wait,
				Insecure:    insecure,
				Reconnect:   reconnect,
				ExtraRoutes: extraRoutes,
			}

			// If no token, check for a saved profile first, then try without token
			// (server will accept if the client IP is whitelisted).
			if token == "" {
				p, err := client.LoadProfile(target)
				if err == nil {
					opts.ServerAddr = p.Host
					opts.Token = p.Token
				} else {
					opts.ServerAddr = target // no token — server checks IP whitelist
				}
			} else {
				opts.ServerAddr = target
			}

			// --github-action: reads AUTH_VPN_API_KEY from the environment and
			// generates a unique ephemeral token per job using GitHub's own env vars
			// (GITHUB_RUN_ID, GITHUB_JOB, GITHUB_RUN_ATTEMPT). Safe for parallel
			// matrix jobs — each gets its own token, revoked on disconnect.
			if githubAction {
				if opts.Token != "" {
					return fmt.Errorf("--github-action and --token are mutually exclusive")
				}
				apiKey = os.Getenv("AUTH_VPN_API_KEY")
				if apiKey == "" {
					return fmt.Errorf("--github-action requires AUTH_VPN_API_KEY env var to be set")
				}
			}

			// --api-key / --github-action: generate a short-lived ephemeral token
			// via the HTTP API. Each job gets its own unique token; it is revoked
			// automatically when the tunnel drops.
			if apiKey != "" {
				if opts.Token != "" {
					return fmt.Errorf("--api-key and --token are mutually exclusive")
				}
				// Resolve the API base URL. Default to localhost so the API key
				// never travels over the public internet in plaintext. Override
				// with --api-url for custom setups (e.g. SSH-tunnelled port).
				resolvedAPIURL := apiURL
				if resolvedAPIURL == "" {
					resolvedAPIURL = "http://localhost:" + strconv.Itoa(apiPort)
				}
				// Refuse plaintext HTTP to non-loopback hosts unless --insecure is set.
				if err := checkAPIURLSafety(resolvedAPIURL, insecure); err != nil {
					return err
				}
				tok, name, err := generateEphemeralToken(resolvedAPIURL, apiKey, githubAction, insecure)
				if err != nil {
					return fmt.Errorf("generate ephemeral token: %w", err)
				}
				opts.Token = tok
				defer revokeEphemeralToken(resolvedAPIURL, apiKey, name, insecure)
				reconnect = false // ephemeral tokens are single-use; reconnect would reuse a revoked token
			}

			if wait {
				fmt.Printf("waiting for server %s ...\n", opts.ServerAddr)
				if err := client.WaitForPing(opts.ServerAddr, 30*time.Second); err != nil {
					return err
				}
			}

			// Proxy mode: --forward localPort:remoteHost:remotePort
			if len(forwardRules) > 0 {
				forwards, err := parseForwardRules(forwardRules)
				if err != nil {
					return err
				}
				if reconnect {
					return client.ConnectProxyWithReconnect(opts, forwards)
				}
				return client.ConnectProxy(opts, forwards)
			}

			if reconnect {
				return client.ConnectWithReconnect(opts)
			}
			return client.Connect(opts)
		},
	}
	cmd.Flags().StringVarP(&token, "token", "t", "", "Auth token")
	cmd.Flags().BoolVar(&background, "background", false, "Suppress interactive output and write PID state file")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait until server is reachable before connecting (CI use)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS certificate verification")
	cmd.Flags().BoolVar(&reconnect, "reconnect", true, "Auto-reconnect with exponential backoff on unexpected drop; use --reconnect=false to disable")
	cmd.Flags().StringArrayVar(&forwardRules, "forward", nil, "Forward local port to remote (localPort:remoteHost:remotePort), e.g. 5432:10.8.0.1:5432")
	cmd.Flags().StringArrayVar(&extraRoutes, "route", nil, "Extra CIDR to route through VPN (e.g. 20.29.40.0/24). Repeatable. Docker containers on the runner benefit automatically.")

	// Hidden flags — for CI/CD use; not shown in --help.
	cmd.Flags().BoolVar(&githubAction, "github-action", false, "Ephemeral token mode for GitHub Actions. Reads AUTH_VPN_API_KEY env var; generates a unique token per job and revokes it on disconnect.")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Generate a unique ephemeral token via the server API (for CI/CD parallel jobs). Revoked automatically on disconnect.")
	cmd.Flags().IntVar(&apiPort, "api-port", 9100, "HTTP API port (default 9100). Used with --api-key/--github-action when --api-url is not set.")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "Full base URL of the server API, e.g. https://<host>:9100. Overrides --api-port.")
	cmd.Flags().MarkHidden("github-action") //nolint:errcheck
	cmd.Flags().MarkHidden("api-key")       //nolint:errcheck
	cmd.Flags().MarkHidden("api-port")      //nolint:errcheck
	cmd.Flags().MarkHidden("api-url")       //nolint:errcheck
	cmd.Flags().MarkHidden("insecure")      //nolint:errcheck
	return cmd
}

// checkAPIURLSafety returns an error if the URL would transmit the API key
// over plaintext HTTP to a non-loopback address, unless insecure is true.
func checkAPIURLSafety(rawURL string, insecure bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid --api-url %q: %w", rawURL, err)
	}
	if strings.ToLower(u.Scheme) == "https" {
		return nil // HTTPS is always safe
	}
	if insecure {
		return nil // user has explicitly opted in to insecure mode
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" ||
		(ip != nil && ip.IsLoopback())
	if !loopback {
		return fmt.Errorf(
			"refusing to send API key over plaintext HTTP to %q\n"+
				"Options:\n"+
				"  1. Pass --insecure to allow plaintext HTTP (only on trusted networks)\n"+
				"  2. Use an SSH tunnel: ssh -L 9100:localhost:9100 user@<vm> -N &\n"+
				"  3. Use HTTPS: --api-url https://<host>:9100",
			host,
		)
	}
	return nil
}

// generateEphemeralToken calls POST /api/tokens on the server to create a
// short-lived token for this specific connection. Used by --api-key and
// --github-action so each parallel CI job gets its own independent token.
func generateEphemeralToken(apiURL, apiKey string, useGithubEnv, insecure bool) (tokenValue, tokenName string, err error) {
	var name string
	if useGithubEnv {
		// Build a unique name from GitHub's own env vars so the token is
		// identifiable in the dashboard: e.g. "gh-12345678-integration-1"
		runID := os.Getenv("GITHUB_RUN_ID")
		job := os.Getenv("GITHUB_JOB")
		attempt := os.Getenv("GITHUB_RUN_ATTEMPT")
		if runID != "" {
			name = fmt.Sprintf("gh-%s-%s-%s", runID, job, attempt)
		}
	}
	if name == "" {
		b := make([]byte, 6)
		if _, err = rand.Read(b); err != nil {
			return "", "", fmt.Errorf("random name: %w", err)
		}
		name = fmt.Sprintf("ephemeral-%x", b)
	}

	type tokenRequest struct {
		Name     string `json:"name"`
		OneTime  bool   `json:"one_time"`
		ExpiresIn string `json:"expires_in,omitempty"` // backstop: token auto-expires if defer never runs (e.g. SIGKILL)
	}
	body, err := json.Marshal(tokenRequest{Name: name, OneTime: false, ExpiresIn: "2h"})
	if err != nil {
		return "", "", fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	c := apiHTTPClient(insecure)
	resp, err := c.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("reach server API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var result struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Token == "" {
		return "", "", fmt.Errorf("server returned an empty token value (unexpected response format)")
	}
	return result.Token, result.Name, nil
}

// apiHTTPClient returns an HTTP client for token API calls.
// When insecure is true it skips TLS verification — needed for self-signed certs
// on the auth-vpn API (which reuses the same cert as the tunnel).
func apiHTTPClient(insecure bool) *http.Client {
	if !insecure {
		return &http.Client{Timeout: 10 * time.Second}
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}

// revokeEphemeralToken deletes the token created by generateEphemeralToken.
// Called via defer so it runs when the tunnel drops, even on error paths.
func revokeEphemeralToken(apiURL, apiKey, name string, insecure bool) {
	// url.PathEscape handles job IDs with dots, spaces, or slashes that would
	// otherwise escape into a different URL path segment.
	req, err := http.NewRequest(http.MethodDelete, apiURL+"/api/tokens/"+url.PathEscape(name), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if resp, err := apiHTTPClient(insecure).Do(req); err == nil {
		resp.Body.Close()
	}
}

// parseForwardRules parses --forward flags of the form localPort:remoteHost:remotePort.
func parseForwardRules(rules []string) ([]client.ForwardRule, error) {
	result := make([]client.ForwardRule, 0, len(rules))
	for _, r := range rules {
		// Split into exactly 3 parts: localPort, remoteHost, remotePort
		// remoteHost may contain colons (IPv6), so split on first and last colon.
		idx := strings.Index(r, ":")
		if idx < 0 {
			return nil, fmt.Errorf("invalid --forward %q: expected localPort:remoteHost:remotePort", r)
		}
		localStr := r[:idx]
		rest := r[idx+1:]

		lastIdx := strings.LastIndex(rest, ":")
		if lastIdx < 0 {
			return nil, fmt.Errorf("invalid --forward %q: expected localPort:remoteHost:remotePort", r)
		}
		remoteHost := rest[:lastIdx]
		remotePortStr := rest[lastIdx+1:]

		localPort, err := strconv.Atoi(localStr)
		if err != nil || localPort < 1 || localPort > 65535 {
			return nil, fmt.Errorf("invalid local port in --forward %q", r)
		}
		remotePort, err := strconv.Atoi(remotePortStr)
		if err != nil || remotePort < 1 || remotePort > 65535 {
			return nil, fmt.Errorf("invalid remote port in --forward %q", r)
		}
		if remoteHost == "" {
			return nil, fmt.Errorf("empty remote host in --forward %q", r)
		}
		result = append(result, client.ForwardRule{
			LocalPort:  localPort,
			RemoteHost: remoteHost,
			RemotePort: remotePort,
		})
	}
	return result, nil
}

// ─── disconnect ───────────────────────────────────────────────────────────────

func disconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect [server]",
		Short: "Disconnect a running background tunnel",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tunnels, err := client.ReadAllMeta()
			if err != nil {
				return err
			}
			if len(tunnels) == 0 {
				fmt.Println("no running tunnel found")
				return nil
			}
			if len(args) == 1 {
				target := args[0]
				for _, m := range tunnels {
					if m.ServerAddr == target {
						return disconnectOne(m)
					}
				}
				return fmt.Errorf("no tunnel found for server %q", target)
			}
			if len(tunnels) > 1 {
				fmt.Println("multiple tunnels running — specify which to disconnect:")
				for _, m := range tunnels {
					fmt.Printf("  auth-vpn disconnect %s\n", m.ServerAddr)
				}
				return nil
			}
			return disconnectOne(tunnels[0])
		},
	}
}

func disconnectOne(meta client.TunnelMeta) error {
	if !client.IsProcessAlive(meta.PID) {
		fmt.Printf("tunnel to %s is not running (stale state — cleaned up)\n", meta.ServerAddr)
		return client.ClearMeta(meta.ServerAddr)
	}
	proc, err := os.FindProcess(meta.PID)
	if err != nil {
		return fmt.Errorf("find process %d: %w", meta.PID, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal process %d: %w", meta.PID, err)
	}
	fmt.Printf("disconnected %s (pid %d)\n", meta.ServerAddr, meta.PID)
	return nil
}

// ─── status ──────────────────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current tunnel status",
		RunE: func(cmd *cobra.Command, args []string) error {
			tunnels, err := client.ReadAllMeta()
			if err != nil {
				return err
			}
			var active []client.TunnelMeta
			for _, m := range tunnels {
				if client.IsProcessAlive(m.PID) {
					active = append(active, m)
				} else {
					_ = client.ClearMeta(m.ServerAddr)
				}
			}
			if len(active) == 0 {
				fmt.Println("status: not connected")
				return nil
			}
			for i, meta := range active {
				uptime := time.Since(meta.ConnectedAt).Round(time.Second)
				if len(active) > 1 {
					fmt.Printf("tunnel %d:\n", i+1)
				} else {
					fmt.Println("status: connected")
				}
				fmt.Printf("  Server       : %s\n", meta.ServerAddr)
				fmt.Printf("  PID          : %d\n", meta.PID)
				fmt.Printf("  Tunnel IP    : %s\n", meta.AssignedIP)
				fmt.Printf("  Server IP    : %s\n", meta.ServerIP)
				fmt.Printf("  Connected at : %s\n", meta.ConnectedAt.Local().Format(time.DateTime))
				fmt.Printf("  Uptime       : %s\n", uptime)
				if i < len(active)-1 {
					fmt.Println()
				}
			}
			return nil
		},
	}
}

// ─── profile ─────────────────────────────────────────────────────────────────

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage saved connection profiles",
	}
	cmd.AddCommand(profileSaveCmd(), profileListCmd())
	return cmd
}

func profileSaveCmd() *cobra.Command {
	var host, token string
	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a connection profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := client.Profile{Name: args[0], Host: host, Token: token}
			if err := client.SaveProfile(p); err != nil {
				return err
			}
			fmt.Printf("profile %q saved — connect with: auth-vpn connect %s\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "Server address host:port (required)")
	cmd.Flags().StringVarP(&token, "token", "t", "", "Auth token (required)")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("token")
	return cmd
}

func profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved profiles",
		Run: func(cmd *cobra.Command, args []string) {
			profiles := client.ListProfiles()
			if len(profiles) == 0 {
				fmt.Println("no profiles saved")
				return
			}
			fmt.Printf("%-20s  %s\n", "NAME", "HOST")
			for _, p := range profiles {
				fmt.Printf("%-20s  %s\n", p.Name, p.Host)
			}
		},
	}
}

// ─── version ──────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("auth-vpn %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		},
	}
}

// ─── update ───────────────────────────────────────────────────────────────────

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update auth-vpn to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			return updater.Run(Version)
		},
	}
}

// ─── hub ──────────────────────────────────────────────────────────────────────

func hubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Central dashboard to manage multiple auth-vpn servers from one place",
	}
	cmd.AddCommand(hubServeCmd())
	return cmd
}

func hubServeCmd() *cobra.Command {
	var port int
	var bind string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the hub dashboard (default: http://127.0.0.1:9200)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := hub.LoadConfig()
			if err != nil {
				return fmt.Errorf("loading hub config: %w", err)
			}
			addr := fmt.Sprintf("%s:%d", bind, port)
			fmt.Printf("Hub dashboard → http://%s\n", addr)
			fmt.Printf("Config file   → %s\n", hub.ConfigPath())
			if len(cfg.Servers) == 0 {
				fmt.Println("No servers registered yet — open the dashboard to add one.")
			}
			h := hub.New(cfg, cfg.HubKey)
			return h.Start(addr)
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 9200, "port to listen on")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "address to bind to")
	return cmd
}
