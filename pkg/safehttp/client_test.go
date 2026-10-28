package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"
)

type lookup func(context.Context, string, string) ([]netip.Addr, error)

func (f lookup) LookupNetIP(ctx context.Context, n, h string) ([]netip.Addr, error) {
	return f(ctx, n, h)
}
func TestSSRFBlockedRanges(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "192.0.2.1", "198.18.0.1", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("allowed %s", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("blocked public %s", s)
		}
	}
}
func TestSSRFDNSPinningAndMixedAnswers(t *testing.T) {
	calls := 0
	g := Guard{Resolver: lookup(func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}), Dial: func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Fatal("DNS resolved again at dial")
		}
		return nil, errors.New("fixture")
	}}
	_, _ = g.DialContext(context.Background(), "tcp", "safe.example:443")
	if calls != 1 {
		t.Fatal("lookup count")
	}
	g.Resolver = lookup(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, e := g.DialContext(context.Background(), "tcp", "mixed.example:443"); !errors.Is(e, ErrBlocked) {
		t.Fatal("mixed DNS answer allowed")
	}
}
func TestSSRFRedirectsAndProtocols(t *testing.T) {
	c := NewClient(time.Second, true)
	for _, raw := range []string{"http://public.example", "file:///etc/passwd", "https://u:p@public.example/", "https://public.example:8443/", "https://169.254.169.254/", "https://[::1]/"} {
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = c.CheckRedirect(&http.Request{URL: u}, nil); e == nil {
			t.Fatal("redirect allowed", raw)
		}
	}
	u, _ := url.Parse("https://public.example/")
	if e := NewClient(time.Second, false).CheckRedirect(&http.Request{URL: u}, nil); !errors.Is(e, http.ErrUseLastResponse) {
		t.Fatal("fixed providers must refuse redirects")
	}
}
