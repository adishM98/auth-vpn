package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/client"
	"github.com/adishM98/auth-vpn/internal/server"
)

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
	root.AddCommand(serverCmd(), connectCmd(), disconnectCmd(), statusCmd(), profileCmd())
	return root
}

// ─── server ──────────────────────────────────────────────────────────────────

func serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage the auth-vpn server",
	}
	cmd.AddCommand(serverInstallCmd(), serverStartCmd(), serverTokensCmd(), serverClientsCmd())
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

func serverStartCmd() *cobra.Command {
	var port int
	var subnet, serverIP, metricsAddr, aclPath, apiKey string

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
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Bearer key for /api/* and /tooljet/* (empty = no auth)")
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
	var token string
	var background, wait, insecure, reconnect bool

	cmd := &cobra.Command{
		Use:   "connect <host:port|profile-name>",
		Short: "Connect to an auth-vpn server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			opts := client.Options{
				Token:      token,
				Background: background,
				Wait:       wait,
				Insecure:   insecure,
				Reconnect:  reconnect,
			}

			// If no token flag, check if target is a saved profile name.
			if token == "" {
				p, err := client.LoadProfile(target)
				if err == nil {
					opts.ServerAddr = p.Host
					opts.Token = p.Token
				} else {
					return fmt.Errorf("no --token provided and profile %q not found", target)
				}
			} else {
				opts.ServerAddr = target
			}

			if wait {
				fmt.Printf("waiting for server %s ...\n", opts.ServerAddr)
				if err := client.WaitForPing(opts.ServerAddr, 30*time.Second); err != nil {
					return err
				}
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
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS certificate verification (dev only)")
	cmd.Flags().BoolVar(&reconnect, "reconnect", false, "Auto-reconnect with exponential backoff on unexpected drop")
	return cmd
}

// ─── disconnect ───────────────────────────────────────────────────────────────

func disconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Disconnect a running background tunnel",
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := client.ReadMeta()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Println("no running tunnel found")
					return nil
				}
				return err
			}
			if !client.IsProcessAlive(meta.PID) {
				fmt.Println("tunnel process is not running (stale state — cleaned up)")
				return client.ClearMeta()
			}
			proc, err := os.FindProcess(meta.PID)
			if err != nil {
				return fmt.Errorf("find process %d: %w", meta.PID, err)
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("signal process %d: %w", meta.PID, err)
			}
			fmt.Printf("disconnected (sent SIGTERM to pid %d)\n", meta.PID)
			return nil
		},
	}
}

// ─── status ──────────────────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current tunnel status",
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := client.ReadMeta()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Println("status: not connected")
					return nil
				}
				return err
			}
			if !client.IsProcessAlive(meta.PID) {
				fmt.Println("status: not connected (stale state file — run 'auth-vpn disconnect' to clean up)")
				return nil
			}
			uptime := time.Since(meta.ConnectedAt).Round(time.Second)
			fmt.Println("status: connected")
			fmt.Printf("  PID          : %d\n", meta.PID)
			fmt.Printf("  Server       : %s\n", meta.ServerAddr)
			fmt.Printf("  Tunnel IP    : %s\n", meta.AssignedIP)
			fmt.Printf("  Server IP    : %s\n", meta.ServerIP)
			fmt.Printf("  Connected at : %s\n", meta.ConnectedAt.Local().Format(time.DateTime))
			fmt.Printf("  Uptime       : %s\n", uptime)
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
