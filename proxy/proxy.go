package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MHS-20/Zodiac/kvclient"
)

const (
	kTaskPrefix         = "/poseidon/tasks/"
	kMappingPrefix      = "/poseidon/mappings/task-worker/"
	kTaskIPPrefix       = "/poseidon/vips/ips/"
)

type Proxy struct {
	client    *kvclient.KVClient
	listeners map[string]net.Listener
	mu        sync.Mutex
	closeCh   chan struct{}
	wg        sync.WaitGroup
}

func New(client *kvclient.KVClient) *Proxy {
	return &Proxy{
		client:    client,
		listeners: make(map[string]net.Listener),
		closeCh:   make(chan struct{}),
	}
}

func (p *Proxy) Start(ctx context.Context) {
	log.Println("Proxy: starting allocation watcher")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	p.syncAllocations(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("Proxy: shutting down")
			p.shutdown()
			return
		case <-ticker.C:
			p.syncAllocations(ctx)
		}
	}
}

func (p *Proxy) syncAllocations(ctx context.Context) {
	pairs, _, err := p.client.List(ctx, kTaskIPPrefix)
	if err != nil {
		log.Printf("Proxy: error listing VIP allocations: %v", err)
		return
	}

	current := make(map[string]string)
	for ip, taskID := range pairs {
		current[ip] = taskID
	}

	p.mu.Lock()
	for ip, taskID := range current {
		key := ip
		if _, ok := p.listeners[key]; !ok {
			go p.startTaskProxy(ctx, ip, taskID, key)
		}
	}
	for key := range p.listeners {
		ip := strings.SplitN(key, ":", 2)[0]
		if _, ok := current[ip]; !ok {
			p.closeListener(key)
		}
	}
	p.mu.Unlock()
}

func (p *Proxy) startTaskProxy(ctx context.Context, ip, taskID, key string) {
	taskData, found, _, err := p.client.Get(ctx, kTaskPrefix+taskID)
	if err != nil || !found {
		log.Printf("Proxy: task %s not found: %v", taskID, err)
		return
	}

	var task struct {
		Ports     []struct {
			ServicePort   int    `json:"ServicePort"`
			ContainerPort int    `json:"ContainerPort"`
			Protocol      string `json:"Protocol"`
		} `json:"Ports"`
		HostPorts map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"HostPorts"`
	}
	if err := json.Unmarshal([]byte(taskData), &task); err != nil {
		log.Printf("Proxy: error decoding task %s: %v", taskID, err)
		return
	}

	workerAddr, _, _, err := p.client.Get(ctx, kMappingPrefix+taskID)
	if err != nil {
		log.Printf("Proxy: worker mapping for task %s not found: %v", taskID, err)
		return
	}
	workerHost := strings.SplitN(workerAddr, ":", 2)[0]

	for _, port := range task.Ports {
		if port.Protocol == "" {
			port.Protocol = "tcp"
		}
		hostPort := lookupHostPort(task.HostPorts, port.ContainerPort)
		if hostPort == "" {
			log.Printf("Proxy: no host port for container port %d (task %s)", port.ContainerPort, taskID)
			continue
		}
		key := net.JoinHostPort(ip, strconv.Itoa(port.ServicePort))
		p.addIP(ip)
		go p.listenAndProxy(key, net.JoinHostPort(workerHost, hostPort), port.Protocol)
	}
}

func lookupHostPort(hostPorts map[string][]struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}, containerPort int) string {
	for key, bindings := range hostPorts {
		if strings.HasPrefix(key, strconv.Itoa(containerPort)+"/") || strings.HasPrefix(key, strconv.Itoa(containerPort)) {
			if len(bindings) > 0 {
				return bindings[0].HostPort
			}
		}
	}
	return ""
}

func (p *Proxy) listenAndProxy(listenAddr, targetAddr, protocol string) {
	p.mu.Lock()
	if _, ok := p.listeners[listenAddr]; ok {
		p.mu.Unlock()
		return
	}

	lc := net.ListenConfig{}
	listener, err := lc.Listen(context.Background(), "tcp", listenAddr)
	if err != nil {
		log.Printf("Proxy: error listening on %s: %v", listenAddr, err)
		p.mu.Unlock()
		return
	}
	p.listeners[listenAddr] = listener
	p.mu.Unlock()

	log.Printf("Proxy: listening on %s -> %s", listenAddr, targetAddr)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-p.closeCh:
					return
				default:
					log.Printf("Proxy: accept error on %s: %v", listenAddr, err)
					return
				}
			}
			go p.proxyConnection(conn, targetAddr)
		}
	}()
}

func (p *Proxy) proxyConnection(src net.Conn, targetAddr string) {
	defer src.Close()

	dst, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		log.Printf("Proxy: error dialing %s: %v", targetAddr, err)
		return
	}
	defer dst.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(dst, src)
		dst.Close()
	}()
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
		src.Close()
	}()
	wg.Wait()
}

func (p *Proxy) closeListener(key string) {
	if lis, ok := p.listeners[key]; ok {
		lis.Close()
		delete(p.listeners, key)
		log.Printf("Proxy: closed listener %s", key)
	}
}

func (p *Proxy) shutdown() {
	p.mu.Lock()
	for key, lis := range p.listeners {
		lis.Close()
		delete(p.listeners, key)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Proxy) addIP(ip string) {
	if runtime.GOOS != "linux" {
		return
	}
	exec.Command("ip", "addr", "add", ip+"/32", "dev", "lo", "2>/dev/null").Run()
}
