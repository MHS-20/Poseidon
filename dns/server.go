package dns

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/MHS-20/poseidon/registry"
	"github.com/miekg/dns"
)

const (
	kDomain = "svc.poseidon.cluster."
	kTTL    = 60
)

type Server struct {
	client   *kvclient.KVClient
	registry *registry.Registry
	domain   string
	srv      *dns.Server
	mu       sync.RWMutex
	aCache   map[string][]net.IP
	srvCache map[string][]dns.SRV
	cacheTTL time.Time
}

func New(client *kvclient.KVClient) *Server {
	return &Server{
		client:   client,
		registry: registry.New(client),
		domain:   kDomain,
		aCache:   make(map[string][]net.IP),
		srvCache: make(map[string][]dns.SRV),
	}
}

func (s *Server) Start(addr string) error {
	s.srv = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: dns.HandlerFunc(s.handleQuery),
	}
	log.Printf("DNS: starting on %s", addr)
	return s.srv.ListenAndServe()
}

func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		s.srv.ShutdownContext(context.Background())
	}
}

func (s *Server) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	for _, q := range r.Question {
		s.answerQuery(m, q)
	}

	w.WriteMsg(m)
}

func (s *Server) answerQuery(m *dns.Msg, q dns.Question) {
	qname := strings.ToLower(q.Name)

	switch q.Qtype {
	case dns.TypeA:
		ips := s.resolveA(qname)
		for _, ip := range ips {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name: qname, Rrtype: dns.TypeA,
					Class: dns.ClassINET, Ttl: kTTL,
				},
				A: ip,
			})
		}
		if len(m.Answer) == 0 {
			m.Rcode = dns.RcodeNameError
		}

	case dns.TypeSRV:
		records := s.resolveSRV(qname)
		for _, r := range records {
			m.Answer = append(m.Answer, &dns.SRV{
				Hdr: dns.RR_Header{
					Name: qname, Rrtype: dns.TypeSRV,
					Class: dns.ClassINET, Ttl: kTTL,
				},
				Priority: r.Priority,
				Weight:   r.Weight,
				Port:     r.Port,
				Target:   r.Target,
			})
		}
		if len(m.Answer) == 0 {
			m.Rcode = dns.RcodeNameError
		}

	case dns.TypePTR:
		domain := s.resolvePTR(qname)
		if domain != "" {
			m.Answer = append(m.Answer, &dns.PTR{
				Hdr: dns.RR_Header{
					Name: qname, Rrtype: dns.TypePTR,
					Class: dns.ClassINET, Ttl: kTTL,
				},
				Ptr: domain,
			})
		} else {
			m.Rcode = dns.RcodeNameError
		}

	default:
		m.Rcode = dns.RcodeNameError
	}
}

func (s *Server) resolveA(name string) []net.IP {
	s.mu.RLock()
	if time.Since(s.cacheTTL) < 30*time.Second {
		if ips, ok := s.aCache[name]; ok {
			s.mu.RUnlock()
			return ips
		}
	}
	s.mu.RUnlock()

	svcName := strings.TrimSuffix(strings.ToLower(name), "."+s.domain)
	if svcName == name {
		return nil
	}

	instances, err := s.registry.LookupHealthy(svcName)
	if err != nil {
		log.Printf("DNS: registry lookup error for %s: %v", svcName, err)
		return nil
	}

	var ips []net.IP
	for _, inst := range instances {
		if ip := net.ParseIP(inst.VirtualIP); ip != nil {
			ips = append(ips, ip)
		}
	}

	s.mu.Lock()
	s.aCache[name] = ips
	s.cacheTTL = time.Now()
	s.mu.Unlock()

	return ips
}

func (s *Server) resolveSRV(name string) []dns.SRV {
	s.mu.RLock()
	if time.Since(s.cacheTTL) < 30*time.Second {
		if records, ok := s.srvCache[name]; ok {
			s.mu.RUnlock()
			return records
		}
	}
	s.mu.RUnlock()

	svcName := strings.TrimSuffix(strings.ToLower(name), "."+s.domain)
	if svcName == name {
		return nil
	}

	instances, err := s.registry.LookupHealthy(svcName)
	if err != nil {
		log.Printf("DNS: SRV lookup error for %s: %v", svcName, err)
		return nil
	}

	var records []dns.SRV
	for priority, inst := range instances {
		host := fmt.Sprintf("%s.%s", strings.ToLower(inst.Name), s.domain)
		for _, p := range inst.Ports {
			records = append(records, dns.SRV{
				Priority: uint16(priority),
				Weight:   10,
				Port:     uint16(p.ServicePort),
				Target:   host,
			})
		}
	}

	s.mu.Lock()
	s.srvCache[name] = records
	s.cacheTTL = time.Now()
	s.mu.Unlock()

	return records
}

func (s *Server) resolvePTR(name string) string {
	ip := extractPTRIP(name)
	if ip == "" {
		return ""
	}

	services, err := s.registry.ListServices()
	if err != nil {
		log.Printf("DNS: PTR list error: %v", err)
		return ""
	}

	for svcName, instances := range services {
		for _, inst := range instances {
			if inst.VirtualIP == ip {
				return fmt.Sprintf("%s.%s", strings.ToLower(svcName), s.domain)
			}
		}
	}
	return ""
}

func extractPTRIP(ptrName string) string {
	parts := strings.Split(strings.TrimSuffix(ptrName, ".in-addr.arpa."), ".")
	if len(parts) != 4 {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s.%s", parts[3], parts[2], parts[1], parts[0])
}
