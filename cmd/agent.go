package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MHS-20/Raft/raft"
	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/Zodiac/kvservice"
	"github.com/MHS-20/poseidon/system"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.Flags().IntP("node-id", "", 1, "Node ID for the embedded Raft node")
	agentCmd.Flags().IntP("raft-port", "", 9001, "HTTP API port for the embedded Zodiac KV store")
	agentCmd.Flags().StringP("data-dir", "", "/tmp/poseidon/agent", "Directory for Raft log storage")
	agentCmd.Flags().StringSliceP("raft-join", "", []string{}, "Existing cluster raft addresses to join")
	agentCmd.Flags().StringP("manifest-dir", "m", "/etc/poseidon/manifests", "Directory containing system task manifests")
	agentCmd.Flags().DurationP("interval", "i", 10*time.Second, "Reconciliation interval")
}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Start the Poseidon system agent (self-hosting).",
	Long: `poseidon agent command.

The agent is a lightweight process that runs directly (not in a container)
and bootstraps the Poseidon cluster by starting system containers
(proxy, DNS, worker) via the Docker SDK.

It reads static manifests from a directory (like k8s static pods)
and ensures the defined system containers are always running.`,
	Run: func(cmd *cobra.Command, args []string) {
		nodeID, _ := cmd.Flags().GetInt("node-id")
		raftPort, _ := cmd.Flags().GetInt("raft-port")
		dataDir, _ := cmd.Flags().GetString("data-dir")
		raftJoins, _ := cmd.Flags().GetStringSlice("raft-join")
		manifestDir, _ := cmd.Flags().GetString("manifest-dir")
		interval, _ := cmd.Flags().GetDuration("interval")

		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Fatalf("unable to create data directory %s: %v", dataDir, err)
		}

		storage, err := raft.NewFileStorage(dataDir)
		if err != nil {
			log.Fatalf("unable to create Raft storage: %v", err)
		}

		peerIDs := []int{}
		ready := make(chan any)

		log.Printf("Starting embedded Zodiac (node %d, raft port %d)", nodeID, raftPort)
		kvs := kvservice.New(nodeID, peerIDs, storage, ready)
		close(ready)

		raftHTTPAddr := fmt.Sprintf("localhost:%d", raftPort)
		kvs.ServeHTTP(raftPort)
		kvs.SetLocalHTTPAddr(raftHTTPAddr)
		log.Printf("Zodiac KV API listening on http://%s", raftHTTPAddr)

		kvCli := kvclient.New([]string{raftHTTPAddr})

		if len(raftJoins) > 0 {
			joinCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := joinRaftCluster(joinCtx, kvs, raftJoins, raftPort, nodeID); err != nil {
				log.Printf("warning: failed to join raft cluster: %v", err)
			}
			cancel()
		}

		_ = kvCli

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent, err := system.NewAgent(manifestDir, interval)
		if err != nil {
			log.Fatalf("unable to create system agent: %v", err)
		}

		go func() {
			if err := agent.Run(ctx); err != nil {
				log.Printf("system agent error: %v", err)
			}
		}()

		log.Printf("Poseidon agent started (node %d, manifests=%s)", nodeID, manifestDir)

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig

		log.Println("Shutting down agent...")
		cancel()
		time.Sleep(2 * time.Second)
	},
}
