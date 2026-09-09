package stealth

import "net/http"

func StripClientHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cf-Connecting-Ip")
		r.Header.Del("X-Real-Ip")
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("Cf-Ipcountry")
		r.Header.Del("X-Forwarded-Proto")
		r.Header.Del("True-Client-Ip")
		r.Header.Del("Cf-Ray")
		next.ServeHTTP(w, r)
	})
}

func FingerprintHeaders(req *http.Request, userAgent string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", "https://www.codebuff.com")
	req.Header.Set("Referer", "https://www.codebuff.com/")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="125", "Not.A/Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}
