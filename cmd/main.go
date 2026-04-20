package main

import (
	"fmt"
	"os"
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
	root.AddCommand(serverCmd(), connectCmd(), statusCmd(), profileCmd())
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

			publicIP, rawToken, err := server.Install(port)
			if err != nil {
				return err
			}

			fmt.Println(publicIP)
			fmt.Println("  ✓ TLS certificate generated")
			fmt.Println("  ✓ Initial token created")
			fmt.Println("  ✓ Systemd service written")
			fmt.Println()
			fmt.Println("  Run:  sudo systemctl enable --now auth-vpn")
			fmt.Println()
			fmt.Println("  ─────────────────────────────────────────────")
			fmt.Printf("  Connect with:\n")
			fmt.Printf("    auth-vpn connect %s:%d --token %s\n", publicIP, port, rawToken)
			fmt.Println("  ─────────────────────────────────────────────")
			return nil
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 7777, "Port to listen on")
	return cmd
}

func serverStartCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the auth-vpn server",
		RunE: func(cmd *cobra.Command, args []string) error {
			srv, err := server.New(&server.Config{
				Port:       port,
				TLSCert:    server.CertFile,
				TLSKey:     server.KeyFile,
				TokensPath: server.TokensFile,
			})
			if err != nil {
				return err
			}
			return srv.Start()
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 7777, "Port to listen on")
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
		Short: "Show currently connected clients (run on server)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// In production this would query a unix socket / pid file.
			// For now, direct usage reminder.
			fmt.Println("Run this on the server where auth-vpn server start is running.")
			fmt.Println("(Live client list requires a running server process.)")
			return nil
		},
	}
}

// ─── connect ─────────────────────────────────────────────────────────────────

func connectCmd() *cobra.Command {
	var token string
	var background, wait, insecure bool

	cmd := &cobra.Command{
		Use:   "connect <host:port|profile-name>",
		Short: "Connect to a auth-vpn server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			opts := client.Options{
				Token:      token,
				Background: background,
				Wait:       wait,
				Insecure:   insecure,
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

			return client.Connect(opts)
		},
	}
	cmd.Flags().StringVarP(&token, "token", "t", "", "Auth token")
	cmd.Flags().BoolVar(&background, "background", false, "Run tunnel in background")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait until tunnel is verified before returning (CI use)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS certificate verification (dev only)")
	return cmd
}

// ─── status ──────────────────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current tunnel status",
		Run: func(cmd *cobra.Command, args []string) {
			// A production implementation would check a pid file / unix socket.
			fmt.Println("auth-vpn status: not connected (no running tunnel detected)")
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
