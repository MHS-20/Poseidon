package proxy

import (
	"context"
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
	"github.com/MHS-20/poseidon/registry"
)

type Proxy struct {
	client    *kvclient.KVClient
	registry  *registry.Registry
	listeners map[string]net.Listener
	mu        sync.Mutex
	closeCh   chan struct{}
	wg        sync.WaitGroup
}

func New(client *kvclient.KVClient) *Proxy {
	return &Proxy{
		client:    client,
		registry:  registry.New(client),
		listeners: make(map[string]net.Listener),
		closeCh:   make(chan struct{}),
	}
}

func (p *Proxy) Start(ctx context.Context) {
	log.Println("Proxy: starting registry watcher")
	p.syncFromRegistry(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Proxy: shutting down")
			p.shutdown()
			return
		case <-ticker.C:
			p.syncFromRegistry(ctx)
		}
	}
}

func (p *Proxy) syncFromRegistry(ctx context.Context) {
	services, err := p.registry.ListServices()
	if err != nil {
		log.Printf("Proxy: error listing registry: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	current := make(map[string]registry.ServiceInstance)
	for _, instances := range services {
		for _, inst := range instances {
			if !inst.Healthy || inst.VirtualIP == "" {
				continue
			}
			current[inst.VirtualIP] = inst
		}
	}

	for ip, inst := range current {
		key := ip
		if _, ok := p.listeners[key]; !ok {
			go p.startListeners(inst)
		}
	}

	for key := range p.listeners {
		ip := strings.SplitN(key, ":", 2)[0]
		if _, ok := current[ip]; !ok {
			p.closeListener(key)
		}
	}
}

func (p *Proxy) startListeners(inst registry.ServiceInstance) {
	for _, port := range inst.Ports {
		listenAddr := net.JoinHostPort(inst.VirtualIP, strconv.Itoa(port.ServicePort))

		workerHost := strings.SplitN(inst.Worker, ":", 2)[0]
		targetAddr := net.JoinHostPort(workerHost, strconv.Itoa(port.ServicePort))

		existingKey, _, _ := net.SplitHostPort(listenAddr)
		_ = existingKey
		key := listenAddr

		p.mu.Lock()
		if _, ok := p.listeners[key]; ok {
			p.mu.Unlock()
			continue
		}
		p.mu.Unlock()

		p.addIP(inst.VirtualIP)
		go p.listenAndProxy(listenAddr, targetAddr, port.Protocol)
	}
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
