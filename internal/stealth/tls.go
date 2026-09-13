package stealth

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// tlsHandshakeTimeout bounds the utls handshake. Because the transport uses
// DialTLSContext, http.Transport's TLSHandshakeTimeout does not apply here —
// the handshake happens inside this dialer, so we budget it ourselves.
const tlsHandshakeTimeout = 10 * time.Second

// dialer implements a DialTLSContext function that uses utls to impersonate a browser TLS fingerprint.
type dialer struct {
	profile            *Profile
	resolveFN          func(ctx context.Context, network, addr string) (net.Conn, error)
	insecureSkipVerify bool
	proxyPool          *ProxyPool
}

// Dial creates a TLS connection using the configured browser fingerprint profile.
func (d *dialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var rawConn net.Conn
	var err error
	var proxyEntry *ProxyEntry

	if d.proxyPool != nil {
		entry := d.proxyPool.Next()
		if entry != nil {
			proxyEntry = entry

			proxyDialer, dialErr := entry.Dialer()
			if dialErr != nil {
				d.proxyPool.MarkFailure(entry)
				proxyEntry = nil
				log.Printf("[stealth] create SOCKS5 dialer failed (%s:%d): %v — falling back to direct", entry.Host, entry.Port, dialErr)
			} else {
				// Prefer the context-aware dial so caller deadlines and the
				// proxy entry's forward-dial timeout both apply.
				if cd, ok := proxyDialer.(proxy.ContextDialer); ok {
					rawConn, err = cd.DialContext(ctx, network, addr)
				} else {
					rawConn, err = proxyDialer.Dial(network, addr)
				}
				if err != nil {
					d.proxyPool.MarkFailure(entry)
					return nil, fmt.Errorf("stealth: SOCKS5 proxy dial to %s failed: %w", addr, err)
				}
			}
		} else {
			log.Printf("[stealth] proxy pool exhausted — falling back to direct for %s", addr)
		}
	}

	if rawConn == nil {
		dialFN := d.resolveFN
		if dialFN == nil {
			dialFN = (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
		rawConn, err = dialFN(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("stealth: tcp dial failed: %w", err)
		}
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		rawConn.Close()
		if proxyEntry != nil {
			d.proxyPool.MarkFailure(proxyEntry)
		}
		return nil, fmt.Errorf("stealth: invalid address %q: %w", addr, err)
	}

	helloID := d.profile.ClientHelloID

	uConn := utls.UClient(rawConn, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: d.insecureSkipVerify,
		MinVersion:         tls.VersionTLS12,
	}, helloID)

	if d.profile.CustomSpec != nil {
		if err := uConn.ApplyPreset(d.profile.CustomSpec); err != nil {
			rawConn.Close()
			if proxyEntry != nil {
				d.proxyPool.MarkFailure(proxyEntry)
			}
			return nil, fmt.Errorf("stealth: apply custom spec failed: %w", err)
		}
	}

	// Perform the TLS handshake with a bounded budget so a stalled
	// proxy or server cannot hang the request for the full client timeout.
	hsCtx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	if err := uConn.HandshakeContext(hsCtx); err != nil {
		rawConn.Close()
		if proxyEntry != nil {
			d.proxyPool.MarkFailure(proxyEntry)
		}
		return nil, fmt.Errorf("stealth: tls handshake failed: %w", err)
	}

	if proxyEntry != nil {
		d.proxyPool.MarkSuccess(proxyEntry)
	}

	return uConn, nil
}

// DialerOption configures the dialer.
type DialerOption func(*dialer)

// WithInsecureSkipVerify enables InsecureSkipVerify on the TLS config.
func WithInsecureSkipVerify() DialerOption {
	return func(d *dialer) {
		d.insecureSkipVerify = true
	}
}

// WithProxyPool attaches a rotating SOCKS5 proxy pool to the dialer.
func WithProxyPool(pool *ProxyPool) DialerOption {
	return func(d *dialer) {
		d.proxyPool = pool
	}
}

// Dialer returns a DialTLSContext function compatible with http.Transport.
func Dialer(profile *Profile, resolver func(context.Context, string, string) (net.Conn, error), opts ...DialerOption) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if profile == nil {
		profile = DefaultProfile
	}
	d := &dialer{
		profile:   profile,
		resolveFN: resolver,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d.Dial
}
