package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MHS-20/Raft/raft"
	"github.com/MHS-20/Zodiac/api"
	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/Zodiac/kvservice"
	"github.com/MHS-20/poseidon/manager"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(managerCmd)
	managerCmd.Flags().StringP("host", "H", "0.0.0.0", "Hostname or IP address")
	managerCmd.Flags().IntP("port", "p", 5555, "Port on which to listen")
	managerCmd.Flags().StringSliceP("workers", "w", []string{"localhost:5556"}, "List of workers on which the manager will schedule tasks.")
	managerCmd.Flags().StringP("scheduler", "s", "epvm", "Name of scheduler to use.")
	managerCmd.Flags().StringP("dbType", "d", "memory", "Type of datastore to use for events and tasks (\"memory\", \"persistent\", or \"raft\")")

	managerCmd.Flags().IntP("node-id", "", 0, "Node ID for Raft consensus (required for raft dbType)")
	managerCmd.Flags().IntP("raft-port", "", 9001, "HTTP API port for the embedded Zodiac KV store")
	managerCmd.Flags().StringP("data-dir", "", "/tmp/poseidon", "Directory for Raft log storage")
	managerCmd.Flags().StringSliceP("raft-join", "", []string{}, "Existing cluster raft addresses to join (e.g. localhost:9001)")
}

var managerCmd = &cobra.Command{
	Use:   "manager",
	Short: "Manager command to operate a Poseidon manager node.",
	Long: `poseidon manager command.

The manager controls the orchestration system and is responsible for:
- Accepting tasks from users
- Scheduling tasks onto worker nodes
- Rescheduling tasks in the event of a node failure
- Periodically polling workers to get task updates

For a replicated setup, use --dbType raft along with --node-id, --raft-port,
--data-dir, and optionally --raft-join to join an existing Raft cluster.`,
	Run: func(cmd *cobra.Command, args []string) {
		host, _ := cmd.Flags().GetString("host")
		port, _ := cmd.Flags().GetInt("port")
		workers, _ := cmd.Flags().GetStringSlice("workers")
		scheduler, _ := cmd.Flags().GetString("scheduler")
		dbType, _ := cmd.Flags().GetString("dbType")

		nodeID, _ := cmd.Flags().GetInt("node-id")
		raftPort, _ := cmd.Flags().GetInt("raft-port")
		dataDir, _ := cmd.Flags().GetString("data-dir")
		raftJoins, _ := cmd.Flags().GetStringSlice("raft-join")

		var kvCli *kvclient.KVClient
		isLeader := func() bool { return true }

		if dbType == "raft" || nodeID > 0 {
			if nodeID == 0 {
				log.Fatal("--node-id is required when using raft dbType")
			}

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

			kvCli = kvclient.New([]string{raftHTTPAddr})
			isLeader = kvs.IsLeader

			if len(raftJoins) > 0 {
				joinCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := joinRaftCluster(joinCtx, kvs, raftJoins, raftPort, nodeID); err != nil {
					log.Printf("warning: failed to join raft cluster: %v", err)
				}
				cancel()
			}

			dbType = "raft"
			log.Printf("Manager is leader: %v", isLeader())
		}

		log.Println("Starting manager.")
		m := manager.New(workers, scheduler, dbType, kvCli, isLeader)
		api := manager.Api{Address: host, Port: port, Manager: m}
		go m.ProcessTasks()
		go m.UpdateTasks()
		go m.DoHealthChecks()
		log.Printf("Starting manager API on http://%s:%d", host, port)
		api.Start()
	},
}

func joinRaftCluster(ctx context.Context, kvs *kvservice.KVService, seeds []string, raftPort int, nodeID int) error {
	raftAddr := kvs.GetRaftListenAddr().String()
	httpAddr := fmt.Sprintf("localhost:%d", raftPort)

	for _, seed := range seeds {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		leader, err := findLeader(ctx, seed, seeds)
		if err != nil {
			log.Printf("warning: could not find leader via seed %s: %v", seed, err)
			continue
		}

		if err := sendJoin(ctx, leader, raftAddr, httpAddr, nodeID); err != nil {
			log.Printf("warning: failed to join via seed %s: %v", seed, err)
			continue
		}
		return nil
	}
	return ctx.Err()
}

func findLeader(ctx context.Context, seedAddr string, allSeeds []string) (string, error) {
	probe := func(addr string) (bool, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		path := fmt.Sprintf("http://%s/status/", addr)
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, path, nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		var status api.StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return false, err
		}
		return status.IsLeader && status.RespStatus == api.StatusOK, nil
	}

	isLeader, err := probe(seedAddr)
	if err == nil && isLeader {
		return seedAddr, nil
	}

	for _, addr := range allSeeds {
		isLeader, err := probe(addr)
		if err == nil && isLeader {
			return addr, nil
		}
	}

	members, err := fetchMembers(ctx, seedAddr)
	if err != nil {
		return "", fmt.Errorf("leader not found")
	}
	for _, m := range members {
		if m.HTTPAddr == "" {
			continue
		}
		isLeader, err := probe(m.HTTPAddr)
		if err == nil && isLeader {
			return m.HTTPAddr, nil
		}
	}
	return "", fmt.Errorf("leader not found among members")
}

func fetchMembers(ctx context.Context, addr string) ([]api.PeerInfo, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	path := fmt.Sprintf("http://%s/members/", addr)
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var mr api.MembersResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	if mr.RespStatus != api.StatusOK {
		return nil, fmt.Errorf("members response status %v", mr.RespStatus)
	}
	return mr.Members, nil
}

func sendJoin(ctx context.Context, leaderAddr string, raftAddr, httpAddr string, nodeID int) error {
	jr := api.JoinRequest{
		ID:       nodeID,
		RaftAddr: raftAddr,
		HTTPAddr: httpAddr,
	}
	log.Printf("sending join request to leader %s (node %d, raft=%s, http=%s)", leaderAddr, nodeID, raftAddr, httpAddr)
	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(jr); err != nil {
		return fmt.Errorf("encode join request: %w", err)
	}

	joinCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path := fmt.Sprintf("http://%s/join/", leaderAddr)
	req, err := http.NewRequestWithContext(joinCtx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var joinResp api.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		return err
	}
	if joinResp.RespStatus != api.StatusOK {
		return fmt.Errorf("join rejected: %v", joinResp.RespStatus)
	}
	log.Printf("Successfully joined raft cluster via leader %s", leaderAddr)
	return nil
}
