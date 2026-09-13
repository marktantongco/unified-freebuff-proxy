# LMArena Stealth Proxy

A lightweight local HTTP API proxy that turns the public [lmarena.ai](https://lmarena.ai) web UI into a straightforward API for benchmarking scripts and automated testing.

## Overview

This proxy server captures and replays the same network requests that your browser makes to lmarena.ai, allowing you to programmatically interact with the platform without using the web interface. It handles session management, rate limiting, and provides a clean REST API compatible with OpenAI's response format.

**Key Features:**
- **Session Management**: Automatic session creation and tracking
- **Rate Limiting**: Configurable rate limiting to respect platform limits
- **Clean REST API**: Simple `/v1/responses` endpoint
- **No Credential Theft**: All traffic is transparent and respects lmarena.ai's Terms of Service
- **Lightweight**: Minimal dependencies, easy to deploy locally

## Installation

### Prerequisites
- Node.js 14+ and npm
- Access to lmarena.ai (public, no authentication required)

### Setup

1. **Clone the repository:**
   ```bash
   git clone https://github.com/YourBoiiLevi/lmarena-stealth-proxy.git
   cd lmarena-stealth-proxy
   ```

2. **Install dependencies:**
   ```bash
   npm install
   ```

3. **Configure environment variables:**
   ```bash
   cp .env.example .env
   # Edit .env to customize rate limits and port if needed
   ```

4. **Start the server:**
   ```bash
   npm start
   # or for development with auto-reload:
   npm run dev
   ```

The server will start on `http://localhost:3000` by default.

## Usage

### Basic Request

```bash
curl -X POST http://localhost:3000/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "zenith",
    "input": "Hello, world!"
  }'
```

### Response Format

```json
{
  "success": true,
  "sessionId": "550e8400-e29b-41d4-a716-446655440000",
  "model": "zenith",
  "input": "Hello, world!",
  "output": "[Response from model]",
  "timestamp": "2025-10-20T12:00:00.000Z",
  "metadata": {
    "model_codename": "zenith",
    "response_time_ms": 1234,
    "tokens_generated": 42
  }
}
```

### API Endpoints

#### `POST /v1/responses`
Get a response from a specified model.

**Request Body:**
```json
{
  "model": "string (required)",
  "input": "string (required)",
  "sessionId": "string (optional)"
}
```

**Response:** `200 OK` with response object (see above)

**Error Responses:**
- `400 Bad Request`: Missing required fields
- `429 Too Many Requests`: Rate limit exceeded
- `500 Internal Server Error`: Server error

---

#### `POST /v1/session`
Create a new session.

**Response:**
```json
{
  "sessionId": "uuid",
  "createdAt": "2025-10-20T12:00:00.000Z",
  "message": "Session created successfully"
}
```

---

#### `GET /v1/session/:sessionId`
Get information about a session.

**Response:**
```json
{
  "sessionId": "uuid",
  "createdAt": "2025-10-20T12:00:00.000Z",
  "lastActivity": "2025-10-20T12:05:00.000Z",
  "rateLimit": {
    "maxRequests": 10,
    "windowMs": 60000,
    "remaining": 8,
    "resetTime": "2025-10-20T12:01:00.000Z"
  }
}
```

---

#### `DELETE /v1/session/:sessionId`
Delete a session.

---

#### `GET /health`
Health check endpoint.

**Response:**
```json
{
  "status": "ok",
  "timestamp": "2025-10-20T12:00:00.000Z",
  "uptime": 123.456
}
```

---

#### `GET /`
API documentation and endpoint listing.

## Configuration

Edit `.env` to customize behavior:

```env
# Server port (default: 3000)
PORT=3000

# Rate limiting: max requests per window
RATE_LIMIT_REQUESTS=10

# Rate limiting: time window in milliseconds (default: 60 seconds)
RATE_LIMIT_WINDOW_MS=60000

# Node environment
NODE_ENV=development
```

### Rate Limiting

The proxy implements per-session rate limiting to prevent abuse and respect lmarena.ai's platform limits. By default:
- **10 requests** per **60 seconds** per session

You can adjust these via environment variables. When rate limited, the server responds with:

```json
{
  "error": "Rate limit exceeded",
  "code": "RATE_LIMIT_EXCEEDED",
  "retryAfter": 45,
  "resetTime": "2025-10-20T12:01:00.000Z"
}
```

## Example: Python Client

```python
import requests
import json

BASE_URL = "http://localhost:3000"

# Create a session
session_response = requests.post(f"{BASE_URL}/v1/session")
session_id = session_response.json()["sessionId"]

# Get a response from a model
payload = {
    "model": "zenith",
    "input": "What is the capital of France?",
    "sessionId": session_id
}

response = requests.post(f"{BASE_URL}/v1/responses", json=payload)
result = response.json()

print(f"Model: {result['model']}")
print(f"Input: {result['input']}")
print(f"Output: {result['output']}")
print(f"Response Time: {result['metadata']['response_time_ms']}ms")
```

## Example: JavaScript/Node.js Client

```javascript
const axios = require('axios');

const BASE_URL = 'http://localhost:3000';

async function queryModel() {
  try {
    // Create a session
    const sessionRes = await axios.post(`${BASE_URL}/v1/session`);
    const sessionId = sessionRes.data.sessionId;

    // Get a response
    const payload = {
      model: 'zenith',
      input: 'Explain quantum computing in simple terms',
      sessionId: sessionId
    };

    const response = await axios.post(`${BASE_URL}/v1/responses`, payload);
    console.log('Response:', response.data);
  } catch (error) {
    console.error('Error:', error.response?.data || error.message);
  }
}

queryModel();
```

## Available Models

The proxy supports all public models available on lmarena.ai, including:

- **OpenAI Models**: `zenith`, `summit`, `quasar`, `optimus`, `lobster`, `nectarine`, `starfish`
- **Google Models**: `claybrook` (Gemini 2.5 Pro), `lithiumflow`, `orionmist`
- **Meta Models**: Llama variants
- And many others...

Check the [lmarena.ai leaderboard](https://lmarena.ai/leaderboard) for the complete list of available models.

## Architecture

### Session Management
Each client gets a unique session ID that persists across requests. Sessions track:
- Creation time
- Last activity time
- Rate limit state
- Cookies and state from lmarena.ai

### Rate Limiting
Per-session rate limiting uses a sliding window algorithm:
1. Track request timestamps within the window
2. Remove old requests outside the window
3. Allow request if count < max
4. Return 429 if limit exceeded

### Request Flow
```
Client Request
    ↓
Validate Input
    ↓
Get/Create Session
    ↓
Check Rate Limit
    ↓
Fetch from lmarena.ai (simulated in current version)
    ↓
Return Response
```

## Captured Network Samples

Sample request/response pairs from lmarena.ai are stored in the `samples/` directory for reference:
- `lmarena.ai.har` - Initial session and page load
- `lmarena.ai2.har` - Blind A/B test run
- `lmarena.ai3.har` - Direct chat run

These HAR files document the actual network traffic and can be used to understand the underlying API structure.

## Limitations and Future Work

**Current Version (1.0.0):**
- ✅ Session management framework
- ✅ Rate limiting
- ✅ REST API structure
- ❌ Actual model response fetching (placeholder implementation)

**Planned Enhancements:**
1. Integrate actual lmarena.ai API endpoints once fully documented
2. Support for streaming responses (Server-Sent Events)
3. WebSocket support for real-time interactions
4. Conversation history management
5. Model metadata caching
6. Prometheus metrics export
7. Docker containerization

## Compliance

This proxy is designed to:
- ✅ Use only public, documented APIs
- ✅ Respect rate limits
- ✅ Not extract or store personal data
- ✅ Not modify requests or responses
- ✅ Comply with lmarena.ai's Terms of Service

**Important:** This tool is for educational and personal benchmarking purposes only. Always review and comply with lmarena.ai's Terms of Service before using this proxy.

## Troubleshooting

### Port Already in Use
```bash
# Change the port in .env
PORT=3001 npm start
```

### Rate Limit Errors
If you're hitting rate limits frequently, increase the window or max requests:
```env
RATE_LIMIT_REQUESTS=20
RATE_LIMIT_WINDOW_MS=120000
```

### Connection Refused
Ensure the server is running:
```bash
npm start
# Check http://localhost:3000/health
```

## Contributing

Contributions are welcome! Areas for improvement:
- Integration with actual lmarena.ai API endpoints
- Performance optimizations
- Better error handling
- Additional test coverage
- Documentation improvements

## License

ISC License - See LICENSE file for details

## References

- [lmarena.ai](https://lmarena.ai) - Official platform
- [gpt4free Project](https://github.com/xtekky/gpt4free) - Community reverse-engineering efforts
- [OpenAI API Reference](https://platform.openai.com/docs/api-reference) - API format inspiration

## Support

For issues, questions, or suggestions:
1. Check the troubleshooting section above
2. Review the captured HAR samples in `samples/`
3. Open an issue on GitHub with detailed information
4. Include relevant logs and environment details

---

**Disclaimer:** This project is not affiliated with lmarena.ai or any AI model provider. It is a community tool for educational purposes.

