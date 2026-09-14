package stealth

import (
	"math/rand"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
)

// ProfileID is a unique identifier for a browser fingerprint profile.
type ProfileID string

const (
	ProfileIDChrome120  ProfileID = "chrome120"
	ProfileIDChrome131  ProfileID = "chrome131"
	ProfileIDChrome133  ProfileID = "chrome133"
	ProfileIDEdge106    ProfileID = "edge106"
	ProfileIDSafari17   ProfileID = "safari17"
	ProfileIDSafari16   ProfileID = "safari16"
	ProfileIDFirefox120 ProfileID = "firefox120"
	ProfileIDFirefox105 ProfileID = "firefox105"
	ProfileIDFirefox102 ProfileID = "firefox102"
	ProfileIDIOSAuto    ProfileID = "ios"
	ProfileIDAndroid    ProfileID = "android"
)

// Profile defines a complete browser TLS fingerprint.
type Profile struct {
	ID              ProfileID
	ClientHelloID   utls.ClientHelloID
	CustomSpec      *utls.ClientHelloSpec
	UserAgent       string
	SecChUA         string
	SecChUAPlatform string
	AcceptLanguage  string
	AcceptEncoding  string
}

// Pre-built browser profiles.
var (
	// ProfileChrome120 mimics Chrome 120 on Windows 10.
	ProfileChrome120 = &Profile{
		ID:              ProfileIDChrome120,
		ClientHelloID:   utls.HelloChrome_120,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		SecChUA:         `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		SecChUAPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileSafari17 mimics Safari 17 on macOS Sonoma.
	ProfileSafari17 = &Profile{
		ID:              ProfileIDSafari17,
		ClientHelloID:   utls.HelloCustom,
		CustomSpec:      safari17Spec(),
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileFirefox120 mimics Firefox 120 on Linux.
	ProfileFirefox120 = &Profile{
		ID:              ProfileIDFirefox120,
		ClientHelloID:   utls.HelloFirefox_120,
		UserAgent:       "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.5",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileChrome131 mimics Chrome 131 on Windows 11.
	ProfileChrome131 = &Profile{
		ID:              ProfileIDChrome131,
		ClientHelloID:   utls.HelloChrome_131,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		SecChUA:         `"Not_A Brand";v="8", "Chromium";v="131", "Google Chrome";v="131"`,
		SecChUAPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileChrome133 mimics Chrome 133 on macOS.
	ProfileChrome133 = &Profile{
		ID:              ProfileIDChrome133,
		ClientHelloID:   utls.HelloChrome_133,
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecChUA:         `"Not_A Brand";v="8", "Chromium";v="133", "Google Chrome";v="133"`,
		SecChUAPlatform: `"macOS"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileEdge106 mimics Edge 106 on Windows 10.
	ProfileEdge106 = &Profile{
		ID:              ProfileIDEdge106,
		ClientHelloID:   utls.HelloEdge_106,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/106.0.0.0 Safari/537.36 Edg/106.0.1370.52",
		SecChUA:         `"Not_A Brand";v="8", "Chromium";v="106", "Microsoft Edge";v="106"`,
		SecChUAPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileFirefox105 mimics Firefox 105 on Windows.
	ProfileFirefox105 = &Profile{
		ID:              ProfileIDFirefox105,
		ClientHelloID:   utls.HelloFirefox_105,
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:105.0) Gecko/20100101 Firefox/105.0",
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.5",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileFirefox102 mimics Firefox 102 ESR on Linux.
	ProfileFirefox102 = &Profile{
		ID:              ProfileIDFirefox102,
		ClientHelloID:   utls.HelloFirefox_102,
		UserAgent:       "Mozilla/5.0 (X11; Linux x86_64; rv:102.0) Gecko/20100101 Firefox/102.0",
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.5",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileSafari16 mimics Safari 16 on macOS Ventura (utls parrot).
	ProfileSafari16 = &Profile{
		ID:              ProfileIDSafari16,
		ClientHelloID:   utls.HelloSafari_16_0,
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15",
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileIOSAuto mimics Mobile Safari (utls iOS parrot).
	ProfileIOSAuto = &Profile{
		ID:              ProfileIDIOSAuto,
		ClientHelloID:   utls.HelloIOS_Auto,
		UserAgent:       "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
		SecChUA:         "",
		SecChUAPlatform: `"iOS"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileAndroid mimics Chrome on Android (OkHttp parrot covers the
	// Android TLS shape; UA advertises mobile Chrome).
	ProfileAndroid = &Profile{
		ID:              ProfileIDAndroid,
		ClientHelloID:   utls.HelloAndroid_11_OkHttp,
		UserAgent:       "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
		SecChUA:         `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		SecChUAPlatform: `"Android"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// ProfileRandom picks a random fingerprint on each connection.
	ProfileRandom = &Profile{
		ID:              "random",
		ClientHelloID:   utls.HelloRandomized,
		UserAgent:       randomUserAgent(),
		SecChUA:         "",
		SecChUAPlatform: "",
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
	}

	// DefaultProfile is used when nil is passed to NewClient.
	DefaultProfile = ProfileChrome120
)

// rotatable is the deterministic rotation order for "rotate" mode.
// Desktop Chrome/Firefox first (highest site compatibility), mobile last.
var rotatable = []*Profile{
	ProfileChrome120,
	ProfileChrome131,
	ProfileChrome133,
	ProfileEdge106,
	ProfileFirefox120,
	ProfileFirefox105,
	ProfileFirefox102,
	ProfileSafari17,
	ProfileSafari16,
	ProfileIOSAuto,
	ProfileAndroid,
}

var rotateCounter uint64

// ProfileByName resolves a config profile name. Known names:
// a ProfileID ("chrome131"), "random", "rotate", "" (default).
// Unknown names fall back to DefaultProfile.
func ProfileByName(name string) *Profile {
	switch ProfileID(name) {
	case "":
		return DefaultProfile
	case "random":
		return RandomProfile()
	case "rotate":
		return RotateProfile()
	}
	for _, p := range rotatable {
		if p.ID == ProfileID(name) {
			return p
		}
	}
	return DefaultProfile
}

// RotateProfile returns the next profile in deterministic rotation order.
// Lock-free; safe for concurrent use by connection pools.
func RotateProfile() *Profile {
	n := atomic.AddUint64(&rotateCounter, 1) - 1
	return rotatable[n%uint64(len(rotatable))]
}

// RandomProfile returns a uniformly random profile (desktop + mobile).
func RandomProfile() *Profile {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return rotatable[rng.Intn(len(rotatable))]
}

// RotatableProfiles returns the rotation order (defensive copy).
func RotatableProfiles() []*Profile {
	out := make([]*Profile, len(rotatable))
	copy(out, rotatable)
	return out
}

func randomUserAgent() string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	agents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
		"Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
	}
	return agents[rng.Intn(len(agents))]
}

func safari17Spec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		CipherSuites: []uint16{
			utls.TLS_AES_128_GCM_SHA256,
			utls.TLS_AES_256_GCM_SHA384,
			utls.TLS_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			utls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			utls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			utls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_RSA_WITH_AES_128_CBC_SHA,
		},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
				utls.CurveP521,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.ECDSAWithSHA1,
				utls.PSSWithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA384,
				utls.PKCS1WithSHA512,
				utls.PKCS1WithSHA1,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519},
			}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.GREASE_PLACEHOLDER,
				utls.VersionTLS13,
				utls.VersionTLS12,
			}},
			&utls.UtlsGREASEExtension{},
			&utls.UtlsGREASEExtension{},
		},
	}
}
