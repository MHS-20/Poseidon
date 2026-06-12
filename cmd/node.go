package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/poseidon/dns"
	"github.com/MHS-20/poseidon/proxy"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(nodeCmd)
	nodeCmd.Flags().StringSliceP("raft", "r", []string{"localhost:9001"}, "Zodiac Raft HTTP addresses")
	nodeCmd.Flags().StringP("listen-addr", "l", "0.0.0.0", "Address for proxy to listen on")
	nodeCmd.Flags().StringP("dns-addr", "", "0.0.0.0:53", "Address for DNS server to listen on")
}

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Start a Poseidon proxy and DNS node.",
	Long: `poseidon node command.

Starts the userspace TCP proxy and embedded DNS resolver for service discovery.
The proxy watches Zodiac KV for VIP allocations and forwards traffic to workers.
The DNS server resolves <name>.svc.poseidon.cluster to the task's VIP.`,
	Run: func(cmd *cobra.Command, args []string) {
		raftAddrs, _ := cmd.Flags().GetStringSlice("raft")
		listenAddr, _ := cmd.Flags().GetString("listen-addr")
		dnsAddr, _ := cmd.Flags().GetString("dns-addr")

		if !hasPort(dnsAddr) {
			dnsAddr = fmt.Sprintf("%s:53", dnsAddr)
		}

		kvCli := kvclient.New(raftAddrs)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		p := proxy.New(kvCli)
		go p.Start(ctx)

		d := dns.New(kvCli)
		go func() {
			if err := d.Start(dnsAddr); err != nil {
				log.Printf("DNS server error: %v", err)
			}
		}()

		log.Printf("Poseidon node started (proxy listen=%s, dns=%s)", listenAddr, dnsAddr)

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig

		log.Println("Shutting down...")
		cancel()
		d.Stop()
	},
}

func hasPort(addr string) bool {
	for i := 0; i < len(addr); i++ {
		if addr[i] == ':' {
			return true
		}
	}
	return false
}
