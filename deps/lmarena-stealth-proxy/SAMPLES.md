# Network Traffic Samples

This directory contains HAR (HTTP Archive) files captured from actual lmarena.ai sessions. These files document the network requests and responses used by the platform and serve as reference material for understanding the API structure.

## Files

### `lmarena.ai.har`
**Initial session and page load**
- Captures the first visit to lmarena.ai
- Includes page load, resource fetching, and initial analytics
- Shows Cloudflare security verification
- Documents session initialization

### `lmarena.ai2.har`
**Blind A/B test run**
- Captures a blind A/B test session where two anonymous models are compared
- Shows battle mode interaction
- Includes voting/feedback submission
- Demonstrates the comparison interface flow

### `lmarena.ai3.har`
**Direct chat run**
- Captures a direct chat session with a single model
- Shows model selection and conversation flow
- Includes streaming response handling
- Documents the chat interface interaction

## Key Endpoints Identified

### Analytics & Telemetry
- **`/ingest/i/v0/e/`** - Event tracking (gzip-compressed)
- **`/ingest/decide/`** - Experiment/feature decision endpoint
- **Google Analytics** - External tracking service

### User Management
- **`/nextjs-api/sign-up`** - User registration/anonymous session creation

### Expected (Not Captured)
Based on the research report and community findings:
- **`/queue/join`** - Join a queue for model interaction
- **`/queue/data`** - Retrieve response data from queue
- **WebSocket endpoints** - For real-time streaming responses

## How to Use These Samples

### 1. Analyze with HAR Viewer
```bash
# Online HAR viewer
# Visit: http://www.softwareishard.com/blog/har-viewer/
# Upload any .har file to inspect requests/responses
```

### 2. Extract Specific Requests
```bash
# Using jq to extract POST requests
jq '.log.entries[] | select(.request.method == "POST")' lmarena.ai3.har

# Extract URLs
jq -r '.log.entries[] | .request.url' lmarena.ai3.har | sort | uniq
```

### 3. Inspect Request/Response Pairs
```bash
# Get a specific request with headers and body
jq '.log.entries[] | select(.request.url | contains("/queue/")) | {
  url: .request.url,
  headers: .request.headers,
  body: .request.postData.text,
  response: .response.content.text
}' lmarena.ai3.har
```

## Request/Response Structure

### Typical Request Format
```json
{
  "method": "POST",
  "url": "https://lmarena.ai/api/endpoint",
  "headers": [
    {
      "name": "content-type",
      "value": "application/json"
    },
    {
      "name": "authorization",
      "value": "Bearer <token>"
    }
  ],
  "postData": {
    "mimeType": "application/json",
    "text": "{...}"
  }
}
```

### Typical Response Format
```json
{
  "status": 200,
  "statusText": "OK",
  "headers": [
    {
      "name": "content-type",
      "value": "application/json"
    }
  ],
  "content": {
    "size": 1234,
    "mimeType": "application/json",
    "text": "{...}"
  }
}
```

## Important Headers

### Request Headers
- **`User-Agent`**: Browser identification
- **`Authorization`**: Session token (if required)
- **`Content-Type`**: Request payload format
- **`Accept-Encoding`**: Compression support (gzip, deflate, br, zstd)
- **`Origin`**: Request origin (https://lmarena.ai)
- **`Referer`**: Referring page

### Response Headers
- **`Content-Type`**: Response format (application/json, text/event-stream, etc.)
- **`Content-Encoding`**: Compression used (gzip, br, etc.)
- **`Cache-Control`**: Caching directives
- **`Set-Cookie`**: Session cookies
- **`X-RateLimit-*`**: Rate limit information

## Session Management

### Session Identification
Sessions are tracked via:
1. **Cookies** - Persistent session identifiers
2. **URL Parameters** - Session hashes in query strings
3. **Request Headers** - Custom session headers

### Session Flow
```
1. Initial page load
   ↓
2. Cloudflare security verification
   ↓
3. Create anonymous session
   ↓
4. Fetch available models
   ↓
5. Select model or start comparison
   ↓
6. Send message/prompt
   ↓
7. Receive response (streaming or complete)
   ↓
8. Submit feedback/voting
```

## Rate Limiting

### Observed Limits
- **Daily limits per model**: ~32 requests (varies by model)
- **Concurrent requests**: Limited
- **Request frequency**: Throttled to prevent abuse

### Rate Limit Headers
```
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 8
X-RateLimit-Reset: 1760961300
```

## Compression

### Supported Encodings
- **gzip**: Standard compression
- **deflate**: Alternative compression
- **br**: Brotli compression
- **zstd**: Zstandard compression

### Decompression
```bash
# Decompress gzip
gunzip < compressed.gz > decompressed.txt

# Decompress with jq
jq '.log.entries[] | select(.response.content.encoding == "gzip")' file.har
```

## Privacy & Security Notes

### What's Included
- ✅ Public API endpoints
- ✅ Request/response structure
- ✅ Header information
- ✅ Timing data

### What's Excluded
- ❌ Personal user data
- ❌ Authentication tokens
- ❌ Session cookies (sanitized)
- ❌ Sensitive information

## Future Enhancements

As the API is better understood, these samples can be used to:
1. Document the complete API specification
2. Create automated test suites
3. Develop client libraries
4. Build monitoring tools
5. Optimize proxy performance

## References

- [HAR 1.2 Specification](http://www.softwareishard.com/blog/har-12-spec/)
- [HTTP Archive Format](https://w3c.github.io/web-performance/specs/HAR/Overview.html)
- [Chrome DevTools Network Tab](https://developer.chrome.com/docs/devtools/network/)

## Contributing

If you have additional HAR samples from different scenarios (streaming responses, error cases, etc.), please consider contributing them to improve the documentation.

---

**Note**: These samples are provided for educational and development purposes. Always respect the platform's Terms of Service when using this data.

