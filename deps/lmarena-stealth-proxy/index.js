const express = require('express');
const axios = require('axios');
const { v4: uuidv4 } = require('uuid');
require('dotenv').config();

const app = express();
const PORT = process.env.PORT || 3000;
const RATE_LIMIT_REQUESTS = parseInt(process.env.RATE_LIMIT_REQUESTS || '10');
const RATE_LIMIT_WINDOW_MS = parseInt(process.env.RATE_LIMIT_WINDOW_MS || '60000');
const LMARENA_BASE_URL = 'https://lmarena.ai';

// Session management
const sessions = new Map();
const rateLimitMap = new Map();

// Middleware
app.use(express.json());

/**
 * Session class to manage user state and cookies
 */
class Session {
  constructor() {
    this.id = uuidv4();
    this.cookies = {};
    this.createdAt = Date.now();
    this.lastActivity = Date.now();
  }

  updateActivity() {
    this.lastActivity = Date.now();
  }

  addCookie(name, value) {
    this.cookies[name] = value;
    this.updateActivity();
  }

  getCookieString() {
    return Object.entries(this.cookies)
      .map(([name, value]) => `${name}=${value}`)
      .join('; ');
  }
}

/**
 * Rate limiter class
 */
class RateLimiter {
  constructor(maxRequests, windowMs) {
    this.maxRequests = maxRequests;
    this.windowMs = windowMs;
    this.requests = [];
  }

  isAllowed() {
    const now = Date.now();
    // Remove old requests outside the window
    this.requests = this.requests.filter(time => now - time < this.windowMs);
    
    if (this.requests.length < this.maxRequests) {
      this.requests.push(now);
      return true;
    }
    return false;
  }

  getRemainingRequests() {
    const now = Date.now();
    this.requests = this.requests.filter(time => now - time < this.windowMs);
    return Math.max(0, this.maxRequests - this.requests.length);
  }

  getResetTime() {
    if (this.requests.length === 0) return 0;
    return this.requests[0] + this.windowMs;
  }
}

/**
 * Get or create a session
 */
function getOrCreateSession(sessionId) {
  if (!sessionId || !sessions.has(sessionId)) {
    const newSession = new Session();
    sessions.set(newSession.id, newSession);
    return newSession;
  }
  const session = sessions.get(sessionId);
  session.updateActivity();
  return session;
}

/**
 * Get or create a rate limiter for a session
 */
function getOrCreateRateLimiter(sessionId) {
  if (!rateLimitMap.has(sessionId)) {
    rateLimitMap.set(sessionId, new RateLimiter(RATE_LIMIT_REQUESTS, RATE_LIMIT_WINDOW_MS));
  }
  return rateLimitMap.get(sessionId);
}

/**
 * Fetch a model response from lmarena.ai
 * This is a placeholder implementation that would need to be updated
 * with the actual API endpoint once identified
 */
async function fetchModelResponse(model, input, session) {
  try {
    // This is a placeholder - the actual implementation would depend on
    // the correct API endpoint from lmarena.ai
    // For now, we'll simulate a response based on the model name
    
    const response = {
      model: model,
      input: input,
      output: `[Simulated response from ${model}] This is a placeholder response. The actual implementation requires the correct lmarena.ai API endpoint.`,
      timestamp: new Date().toISOString(),
      sessionId: session.id,
      metadata: {
        model_codename: model,
        response_time_ms: Math.random() * 5000,
        tokens_generated: Math.floor(Math.random() * 100),
      }
    };

    return response;
  } catch (error) {
    throw new Error(`Failed to fetch response from model ${model}: ${error.message}`);
  }
}

/**
 * POST /v1/responses - Main endpoint for getting model responses
 * Request body: { model: "string", input: "string", sessionId?: "string" }
 * Response: { model, input, output, timestamp, sessionId, metadata }
 */
app.post('/v1/responses', async (req, res) => {
  try {
    const { model, input, sessionId } = req.body;

    // Validation
    if (!model || !input) {
      return res.status(400).json({
        error: 'Missing required fields: model and input',
        code: 'INVALID_REQUEST'
      });
    }

    // Get or create session
    const session = getOrCreateSession(sessionId);

    // Check rate limit
    const limiter = getOrCreateRateLimiter(session.id);
    if (!limiter.isAllowed()) {
      const resetTime = limiter.getResetTime();
      return res.status(429).json({
        error: 'Rate limit exceeded',
        code: 'RATE_LIMIT_EXCEEDED',
        retryAfter: Math.ceil((resetTime - Date.now()) / 1000),
        resetTime: new Date(resetTime).toISOString()
      });
    }

    // Fetch response from model
    const response = await fetchModelResponse(model, input, session);

    // Return response with session info
    res.status(200).json({
      success: true,
      sessionId: session.id,
      ...response
    });

  } catch (error) {
    console.error('Error in /v1/responses:', error);
    res.status(500).json({
      error: error.message,
      code: 'INTERNAL_SERVER_ERROR'
    });
  }
});

/**
 * GET /v1/session/:sessionId - Get session information
 */
app.get('/v1/session/:sessionId', (req, res) => {
  const { sessionId } = req.params;
  
  if (!sessions.has(sessionId)) {
    return res.status(404).json({
      error: 'Session not found',
      code: 'SESSION_NOT_FOUND'
    });
  }

  const session = sessions.get(sessionId);
  const limiter = getOrCreateRateLimiter(sessionId);

  res.status(200).json({
    sessionId: session.id,
    createdAt: new Date(session.createdAt).toISOString(),
    lastActivity: new Date(session.lastActivity).toISOString(),
    rateLimit: {
      maxRequests: RATE_LIMIT_REQUESTS,
      windowMs: RATE_LIMIT_WINDOW_MS,
      remaining: limiter.getRemainingRequests(),
      resetTime: new Date(limiter.getResetTime()).toISOString()
    }
  });
});

/**
 * POST /v1/session - Create a new session
 */
app.post('/v1/session', (req, res) => {
  const session = getOrCreateSession(null);
  
  res.status(201).json({
    sessionId: session.id,
    createdAt: new Date(session.createdAt).toISOString(),
    message: 'Session created successfully'
  });
});

/**
 * DELETE /v1/session/:sessionId - Delete a session
 */
app.delete('/v1/session/:sessionId', (req, res) => {
  const { sessionId } = req.params;
  
  if (!sessions.has(sessionId)) {
    return res.status(404).json({
      error: 'Session not found',
      code: 'SESSION_NOT_FOUND'
    });
  }

  sessions.delete(sessionId);
  rateLimitMap.delete(sessionId);

  res.status(200).json({
    message: 'Session deleted successfully',
    sessionId: sessionId
  });
});

/**
 * GET /health - Health check endpoint
 */
app.get('/health', (req, res) => {
  res.status(200).json({
    status: 'ok',
    timestamp: new Date().toISOString(),
    uptime: process.uptime()
  });
});

/**
 * GET / - Root endpoint with API documentation
 */
app.get('/', (req, res) => {
  res.status(200).json({
    name: 'LMArena Stealth Proxy',
    version: '1.0.0',
    description: 'A local HTTP API proxy for lmarena.ai',
    endpoints: {
      'POST /v1/responses': {
        description: 'Get a response from a model',
        body: { model: 'string', input: 'string', sessionId: 'string (optional)' },
        response: { model: 'string', input: 'string', output: 'string', timestamp: 'ISOString', sessionId: 'string', metadata: 'object' }
      },
      'POST /v1/session': {
        description: 'Create a new session',
        response: { sessionId: 'string', createdAt: 'ISOString' }
      },
      'GET /v1/session/:sessionId': {
        description: 'Get session information',
        response: { sessionId: 'string', createdAt: 'ISOString', lastActivity: 'ISOString', rateLimit: 'object' }
      },
      'DELETE /v1/session/:sessionId': {
        description: 'Delete a session'
      },
      'GET /health': {
        description: 'Health check endpoint'
      }
    },
    environment: {
      PORT: PORT,
      RATE_LIMIT_REQUESTS: RATE_LIMIT_REQUESTS,
      RATE_LIMIT_WINDOW_MS: RATE_LIMIT_WINDOW_MS
    }
  });
});

/**
 * Error handling middleware
 */
app.use((err, req, res, next) => {
  console.error('Unhandled error:', err);
  res.status(500).json({
    error: 'Internal server error',
    code: 'INTERNAL_SERVER_ERROR',
    message: process.env.NODE_ENV === 'development' ? err.message : undefined
  });
});

/**
 * Start the server
 */
const server = app.listen(PORT, () => {
  console.log(`LMArena Stealth Proxy listening on http://localhost:${PORT}`);
  console.log(`Rate limiting: ${RATE_LIMIT_REQUESTS} requests per ${RATE_LIMIT_WINDOW_MS}ms`);
  console.log(`Environment: ${process.env.NODE_ENV || 'development'}`);
});

module.exports = { app, server };

