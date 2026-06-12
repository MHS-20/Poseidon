package dns

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/MHS-20/Zodiac/kvclient"
	"github.com/miekg/dns"
)

const (
	kTaskPrefix = "/poseidon/tasks/"
	kDomain     = "svc.poseidon.cluster."
)

type Server struct {
	client *kvclient.KVClient
	domain string
	srv    *dns.Server
	mu     sync.Mutex
	cache  map[string]string
}

func New(client *kvclient.KVClient) *Server {
	return &Server{
		client: client,
		domain: kDomain,
		cache:  make(map[string]string),
	}
}

func (s *Server) Start(addr string) error {
	s.mu.Lock()
	s.srv = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: dns.HandlerFunc(s.handleQuery),
	}
	s.mu.Unlock()
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
		switch q.Qtype {
		case dns.TypeA:
			ip := s.resolve(q.Name)
			if ip != "" {
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    60,
					},
					A: net.ParseIP(ip),
				})
			} else {
				m.Rcode = dns.RcodeNameError
			}
		default:
			m.Rcode = dns.RcodeNameError
		}
	}

	w.WriteMsg(m)
}

func (s *Server) resolve(name string) string {
	s.mu.Lock()
	ip, ok := s.cache[name]
	s.mu.Unlock()
	if ok {
		return ip
	}

	taskName := strings.TrimSuffix(strings.ToLower(name), "."+s.domain)
	if taskName == name {
		return ""
	}

	ip = s.lookupTaskVIP(taskName)
	if ip != "" {
		s.mu.Lock()
		s.cache[name] = ip
		s.mu.Unlock()
	}
	return ip
}

func (s *Server) lookupTaskVIP(taskName string) string {
	ctx := context.Background()
	pairs, _, err := s.client.List(ctx, kTaskPrefix)
	if err != nil {
		log.Printf("DNS: error listing tasks: %v", err)
		return ""
	}

	for _, data := range pairs {
		var t struct {
			Name      string `json:"Name"`
			VirtualIP string `json:"VirtualIP"`
		}
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			continue
		}
		if strings.EqualFold(t.Name, taskName) && t.VirtualIP != "" {
			return t.VirtualIP
		}
	}
	return ""
}
