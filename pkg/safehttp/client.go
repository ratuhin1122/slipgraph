// Package safehttp confines outbound HTTPS to public, resolved IP addresses.
package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

var ErrBlocked = errors.New("outbound destination blocked")
var reserved = []netip.Prefix{
	netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Zone() != "" || !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func ValidateURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ErrBlocked
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && !Public(ip) {
		return ErrBlocked
	}
	return nil
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Guard struct {
	Resolver Resolver
	Dial     func(context.Context, string, string) (net.Conn, error)
}

func (g Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, ErrBlocked
	}
	host, port, e := net.SplitHostPort(address)
	if e != nil || port != "443" {
		return nil, ErrBlocked
	}
	resolver := g.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, e := resolver.LookupNetIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, ErrBlocked
	}
	// Reject mixed answers; pin the validated IP in the socket dial to prevent rebinding.
	for _, ip := range ips {
		if !Public(ip) {
			return nil, ErrBlocked
		}
	}
	dial := g.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	var last error
	for _, ip := range ips {
		conn, e := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if e == nil {
			return conn, nil
		}
		last = e
	}
	return nil, last
}

type transport struct{ base *http.Transport }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if e := ValidateURL(r.URL); e != nil {
		return nil, e
	}
	return t.base.RoundTrip(r)
}
func NewClient(timeout time.Duration, redirects bool) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = (Guard{}).DialContext
	base.TLSHandshakeTimeout = 5 * time.Second
	base.ResponseHeaderTimeout = 15 * time.Second
	base.MaxResponseHeaderBytes = 32768
	return &http.Client{Transport: transport{base}, Timeout: timeout, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if !redirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= 3 {
			return ErrBlocked
		}
		return ValidateURL(r.URL)
	}}
}
