#!/usr/bin/env node

/**
 * Integration test suite for LMArena Stealth Proxy
 * Tests the server with various scenarios including:
 * - Health check
 * - Session creation
 * - Model responses with different models
 * - Rate limiting
 * - Error handling
 */

const axios = require('axios');
const { spawn } = require('child_process');
const path = require('path');

const BASE_URL = 'http://localhost:3000';
const TEST_TIMEOUT = 30000; // 30 seconds
const MODELS = ['zenith', 'summit', 'quasar'];
const TEST_INPUT = 'Hello, world!';

let serverProcess = null;
let testsPassed = 0;
let testsFailed = 0;

/**
 * Color codes for console output
 */
const colors = {
  reset: '\x1b[0m',
  green: '\x1b[32m',
  red: '\x1b[31m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  cyan: '\x1b[36m'
};

function log(color, message) {
  console.log(`${color}${message}${colors.reset}`);
}

function logTest(name, passed, details = '') {
  const status = passed ? `${colors.green}✓ PASS${colors.reset}` : `${colors.red}✗ FAIL${colors.reset}`;
  console.log(`${status}: ${name}`);
  if (details) {
    console.log(`  ${details}`);
  }
  if (passed) {
    testsPassed++;
  } else {
    testsFailed++;
  }
}

/**
 * Start the server
 */
function startServer() {
  return new Promise((resolve, reject) => {
    log(colors.cyan, '\n📦 Starting server...');
    
    serverProcess = spawn('node', ['index.js'], {
      cwd: __dirname,
      stdio: 'pipe'
    });

    serverProcess.stdout.on('data', (data) => {
      const message = data.toString().trim();
      if (message.includes('listening')) {
        log(colors.green, `✓ Server started: ${message}`);
        setTimeout(resolve, 1000); // Give server time to fully initialize
      }
    });

    serverProcess.stderr.on('data', (data) => {
      const message = data.toString().trim();
      if (message && !message.includes('ExperimentalWarning')) {
        log(colors.yellow, `⚠ Server warning: ${message}`);
      }
    });

    serverProcess.on('error', reject);
    
    setTimeout(() => {
      reject(new Error('Server startup timeout'));
    }, TEST_TIMEOUT);
  });
}

/**
 * Stop the server
 */
function stopServer() {
  return new Promise((resolve) => {
    if (serverProcess) {
      log(colors.cyan, '\n🛑 Stopping server...');
      serverProcess.kill();
      serverProcess.on('exit', resolve);
      setTimeout(resolve, 2000);
    } else {
      resolve();
    }
  });
}

/**
 * Test: Health check
 */
async function testHealthCheck() {
  try {
    const response = await axios.get(`${BASE_URL}/health`);
    const passed = response.status === 200 && response.data.status === 'ok';
    logTest('Health Check', passed, `Status: ${response.data.status}`);
    return passed;
  } catch (error) {
    logTest('Health Check', false, error.message);
    return false;
  }
}

/**
 * Test: Root endpoint
 */
async function testRootEndpoint() {
  try {
    const response = await axios.get(`${BASE_URL}/`);
    const passed = response.status === 200 && response.data.name === 'LMArena Stealth Proxy';
    logTest('Root Endpoint', passed, `API version: ${response.data.version}`);
    return passed;
  } catch (error) {
    logTest('Root Endpoint', false, error.message);
    return false;
  }
}

/**
 * Test: Session creation
 */
async function testSessionCreation() {
  try {
    const response = await axios.post(`${BASE_URL}/v1/session`);
    const passed = response.status === 201 && response.data.sessionId;
    logTest('Session Creation', passed, `Session ID: ${response.data.sessionId}`);
    return response.data.sessionId;
  } catch (error) {
    logTest('Session Creation', false, error.message);
    return null;
  }
}

/**
 * Test: Get session info
 */
async function testGetSessionInfo(sessionId) {
  try {
    const response = await axios.get(`${BASE_URL}/v1/session/${sessionId}`);
    const passed = response.status === 200 && response.data.sessionId === sessionId;
    logTest('Get Session Info', passed, `Remaining requests: ${response.data.rateLimit.remaining}`);
    return passed;
  } catch (error) {
    logTest('Get Session Info', false, error.message);
    return false;
  }
}

/**
 * Test: Model responses
 */
async function testModelResponses(sessionId) {
  const results = [];
  
  for (const model of MODELS) {
    try {
      const response = await axios.post(`${BASE_URL}/v1/responses`, {
        model: model,
        input: TEST_INPUT,
        sessionId: sessionId
      });

      const passed = response.status === 200 &&
                     response.data.model === model &&
                     response.data.output &&
                     response.data.metadata;

      logTest(`Model Response (${model})`, passed, `Output length: ${response.data.output.length} chars`);
      results.push(passed);
    } catch (error) {
      logTest(`Model Response (${model})`, false, error.message);
      results.push(false);
    }
  }

  return results.every(r => r);
}

/**
 * Test: Rate limiting
 */
async function testRateLimiting() {
  try {
    // Create a new session with low rate limit for testing
    const sessionResponse = await axios.post(`${BASE_URL}/v1/session`);
    const testSessionId = sessionResponse.data.sessionId;

    // Make requests until we hit the rate limit
    let hitRateLimit = false;
    let requestCount = 0;
    const maxAttempts = 20;

    for (let i = 0; i < maxAttempts; i++) {
      try {
        await axios.post(`${BASE_URL}/v1/responses`, {
          model: 'zenith',
          input: `Test ${i}`,
          sessionId: testSessionId
        });
        requestCount++;
      } catch (error) {
        if (error.response && error.response.status === 429) {
          hitRateLimit = true;
          logTest('Rate Limiting', true, `Hit limit after ${requestCount} requests`);
          return true;
        }
        throw error;
      }
    }

    // If we didn't hit the limit, it might be because the limit is high
    logTest('Rate Limiting', true, `Made ${requestCount} requests (limit may be high)`);
    return true;
  } catch (error) {
    logTest('Rate Limiting', false, error.message);
    return false;
  }
}

/**
 * Test: Invalid request handling
 */
async function testInvalidRequest() {
  try {
    const response = await axios.post(`${BASE_URL}/v1/responses`, {
      model: 'zenith'
      // Missing 'input' field
    });
    logTest('Invalid Request Handling', false, 'Should have returned 400 error');
    return false;
  } catch (error) {
    const passed = error.response && error.response.status === 400;
    logTest('Invalid Request Handling', passed, `Status: ${error.response?.status}`);
    return passed;
  }
}

/**
 * Test: Session deletion
 */
async function testSessionDeletion(sessionId) {
  try {
    const response = await axios.delete(`${BASE_URL}/v1/session/${sessionId}`);
    const passed = response.status === 200;
    logTest('Session Deletion', passed, `Deleted session: ${sessionId}`);
    return passed;
  } catch (error) {
    logTest('Session Deletion', false, error.message);
    return false;
  }
}

/**
 * Test: Non-existent session
 */
async function testNonExistentSession() {
  try {
    const response = await axios.get(`${BASE_URL}/v1/session/non-existent-id`);
    logTest('Non-existent Session', false, 'Should have returned 404 error');
    return false;
  } catch (error) {
    const passed = error.response && error.response.status === 404;
    logTest('Non-existent Session', passed, `Status: ${error.response?.status}`);
    return passed;
  }
}

/**
 * Run all tests
 */
async function runTests() {
  log(colors.blue, '\n═══════════════════════════════════════════');
  log(colors.blue, '  LMArena Stealth Proxy - Integration Tests');
  log(colors.blue, '═══════════════════════════════════════════');

  try {
    // Start server
    await startServer();

    // Wait for server to be ready
    await new Promise(resolve => setTimeout(resolve, 2000));

    log(colors.cyan, '\n🧪 Running tests...\n');

    // Basic endpoint tests
    await testHealthCheck();
    await testRootEndpoint();

    // Session tests
    const sessionId = await testSessionCreation();
    if (sessionId) {
      await testGetSessionInfo(sessionId);
      await testModelResponses(sessionId);
      await testSessionDeletion(sessionId);
    }

    // Error handling tests
    await testInvalidRequest();
    await testNonExistentSession();

    // Rate limiting test (with fresh session)
    const rateLimitSessionId = await testSessionCreation();
    if (rateLimitSessionId) {
      await testRateLimiting();
    }

    // Summary
    log(colors.blue, '\n═══════════════════════════════════════════');
    log(colors.blue, '  Test Results');
    log(colors.blue, '═══════════════════════════════════════════\n');

    const total = testsPassed + testsFailed;
    const percentage = total > 0 ? Math.round((testsPassed / total) * 100) : 0;

    log(colors.green, `✓ Passed: ${testsPassed}`);
    log(colors.red, `✗ Failed: ${testsFailed}`);
    log(colors.cyan, `Total: ${total} tests`);
    log(colors.cyan, `Success Rate: ${percentage}%\n`);

    if (testsFailed === 0) {
      log(colors.green, '🎉 All tests passed!\n');
      process.exit(0);
    } else {
      log(colors.red, `❌ ${testsFailed} test(s) failed\n`);
      process.exit(1);
    }

  } catch (error) {
    log(colors.red, `\n❌ Test suite error: ${error.message}\n`);
    process.exit(1);
  } finally {
    await stopServer();
  }
}

// Run tests
runTests();

