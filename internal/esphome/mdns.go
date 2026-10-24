package esphome

import (
	"log"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	serviceType = "_esphomelib._tcp.local."
	servicesAll = "_services._dns-sd._udp.local."
	mdnsTTL     = 120
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Advertise answers mDNS queries for the ESPHome API service on iface so
// Home Assistant discovers the proxy. It blocks until the socket fails.
func (s *Server) Advertise(iface string, port int) error {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, mdnsGroup)
	if err != nil {
		return err
	}
	defer conn.Close()

	instance := s.Info.Name + "." + serviceType
	host := s.Info.Name + ".local."
	announce := func() {
		if pkt, err := s.mdnsResponse(0, ifi, instance, host, port); err == nil {
			conn.WriteToUDP(pkt, mdnsGroup)
		}
	}
	go func() {
		for _, d := range []time.Duration{0, time.Second, 3 * time.Second} {
			time.Sleep(d)
			announce()
		}
	}()

	buf := make([]byte, 9000)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil || h.Response {
			continue
		}
		qs, err := p.AllQuestions()
		if err != nil {
			continue
		}
		match := false
		for _, q := range qs {
			name := strings.ToLower(q.Name.String())
			if name == serviceType || name == strings.ToLower(instance) || name == strings.ToLower(host) || name == servicesAll {
				match = true
			}
		}
		if !match {
			continue
		}
		pkt, err := s.mdnsResponse(h.ID, ifi, instance, host, port)
		if err != nil {
			log.Printf("mdns: %v", err)
			continue
		}
		if src.Port != 5353 { // legacy unicast query: answer the sender directly
			conn.WriteToUDP(pkt, src)
		} else {
			conn.WriteToUDP(pkt, mdnsGroup)
		}
	}
}

func (s *Server) mdnsResponse(id uint16, ifi *net.Interface, instance, host string, port int) ([]byte, error) {
	var ip4 [4]byte
	if addrs, err := ifi.Addrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				copy(ip4[:], n.IP.To4())
				break
			}
		}
	}
	svc := dnsmessage.MustNewName(serviceType)
	inst, err := dnsmessage.NewName(instance)
	if err != nil {
		return nil, err
	}
	hst, err := dnsmessage.NewName(host)
	if err != nil {
		return nil, err
	}
	hdr := func(name dnsmessage.Name, t dnsmessage.Type, flush bool) dnsmessage.ResourceHeader {
		class := dnsmessage.ClassINET
		if flush {
			class |= 0x8000 // cache-flush: unique record
		}
		return dnsmessage.ResourceHeader{Name: name, Type: t, Class: class, TTL: mdnsTTL}
	}
	i := s.Info
	txt := []string{
		"version=" + i.Version,
		"mac=" + strings.ToLower(strings.ReplaceAll(i.MAC, ":", "")),
		"platform=openhub2",
		"board=xiaomi-hub2",
		"network=wifi",
		"friendly_name=" + i.FriendlyName,
		"project_name=" + i.ProjectName,
		"project_version=" + i.ProjectVersion,
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, Authoritative: true})
	b.EnableCompression()
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	if err := b.PTRResource(hdr(dnsmessage.MustNewName(servicesAll), dnsmessage.TypePTR, false), dnsmessage.PTRResource{PTR: svc}); err != nil {
		return nil, err
	}
	if err := b.PTRResource(hdr(svc, dnsmessage.TypePTR, false), dnsmessage.PTRResource{PTR: inst}); err != nil {
		return nil, err
	}
	if err := b.SRVResource(hdr(inst, dnsmessage.TypeSRV, true), dnsmessage.SRVResource{Port: uint16(port), Target: hst}); err != nil {
		return nil, err
	}
	if err := b.TXTResource(hdr(inst, dnsmessage.TypeTXT, true), dnsmessage.TXTResource{TXT: txt}); err != nil {
		return nil, err
	}
	if err := b.AResource(hdr(hst, dnsmessage.TypeA, true), dnsmessage.AResource{A: ip4}); err != nil {
		return nil, err
	}
	return b.Finish()
}
