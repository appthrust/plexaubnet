import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

// Test configuration
export const options = {
  scenarios: {
    poolclaim_ops: {
      executor: 'ramping-arrival-rate',
      // Gradually increasing RPS profile
      startRate: 5,  // Initial requests per second
      timeUnit: '1s',
      // Different stage settings for SMOKE/SOAK modes
      stages: __ENV.SOAK === 'true' ? [
        // SOAK: Slowly increase to target RPS over 10 minutes, then maintain
        { duration: '10m', target: Number(__ENV.MAX_RPS_CLAIM || 20) },
        { duration: __ENV.DURATION || '30m', target: Number(__ENV.MAX_RPS_CLAIM || 20) }
      ] : [
        // SMOKE: Increase to target RPS over 60 seconds, maintain for remaining time
        { duration: '60s', target: Number(__ENV.MAX_RPS_CLAIM || 10) },
        { duration: __ENV.DURATION || '5m', target: Number(__ENV.MAX_RPS_CLAIM || 10) }
      ],
      preAllocatedVUs: __ENV.SOAK === 'true' ? 10 : 5,
      maxVUs: __ENV.SOAK === 'true' ? 100 : 50,
      gracefulStop: '1m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'], // Allow error rate less than 1%
    http_req_duration: ['p(95)<5000'], // 95th percentile under 5 seconds
  },
  // Skip TLS certificate verification
  insecureSkipTLSVerify: true,
  // Keep response bodies for error response display
  discardResponseBodies: false,
};

// Auto-detect API connection
let defaultApiBase = 'http://localhost:8080';
try {
  const tokenPath = '/var/run/secrets/kubernetes.io/serviceaccount/token';
  const token = open(tokenPath);
  if (token) {
    console.log('Kubernetes service account token found - using in-cluster API');
    defaultApiBase = 'https://kubernetes.default.svc';
  }
} catch (e) {
  console.log('Not running in Kubernetes, using localhost API');
}

// Test variables
const API_BASE = __ENV.API_BASE || defaultApiBase;
const NAMESPACE = __ENV.NAMESPACE || 'default';
const POOL_NAME = __ENV.POOL_NAME || 'pool-for-claim-load';

// Headers
const headers = {
  'Content-Type': 'application/json',
};

// Authentication settings when running in Kubernetes with k6-operator
let authHeaders = {};
try {
  const tokenPath = '/var/run/secrets/kubernetes.io/serviceaccount/token';
  const token = open(tokenPath);
  if (token) {
    authHeaders = {
      'Authorization': `Bearer ${token}`,
    };
    console.log('Kubernetes service account token found and using for auth');
  }
} catch (e) {
  console.log('Not running in Kubernetes, using default settings');
}

// Merge auth headers
Object.assign(headers, authHeaders);

// Common request parameters
const commonParams = {
  headers: headers,
  timeout: '10s',
};

// Variables for error retry
let consecutiveErrors = 0;
const MAX_CONSECUTIVE_ERRORS = 5;
const ERROR_BACKOFF_SECONDS = 1;

// Create pool (if it doesn't exist)
function ensurePool() {
  const poolManifest = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: POOL_NAME,
      namespace: NAMESPACE,
    },
    spec: {
      cidr: '172.16.0.0/16',
      defaultBlockSize: 24,
      strategy: "Linear"
    }
  });

  const url = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools`;

  // Check if pool exists with GET
  const checkRes = http.get(`${url}/${POOL_NAME}`, commonParams);
  if (checkRes.status === 404) {
    console.log(`Creating pool ${POOL_NAME} for claims`);
    const createRes = http.post(url, poolManifest, commonParams);
    check(createRes, {
      'Pool created for claims': (r) => r.status === 201 || r.status === 200,
    });
    sleep(2); // Wait for pool to be created
  } else {
    console.log(`Pool ${POOL_NAME} already exists`);
  }
}

// Verify pool at test start
export function setup() {
  console.log(`Starting SubnetPoolClaim CRUD test against ${API_BASE}`);
  ensurePool();
  return { startTime: new Date().toISOString() };
}

// Main test function
export default function() {
  const claimName = `load-test-claim-${randomString(6)}`.toLowerCase();
  
  // Create - create SubnetPoolClaim
  const createPayload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPoolClaim',
    metadata: {
      name: claimName,
      namespace: NAMESPACE,
    },
    spec: {
      parentPoolRef: POOL_NAME,
      desiredBlockSize: 24,
      description: `Load test claim created by k6 at ${new Date().toISOString()}`
    }
  });
  
  let createUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpoolclaims`;
  let createRes = http.post(createUrl, createPayload, commonParams);
  
  // Check request result - count and process errors
  const createSuccess = check(createRes, {
    'PoolClaim created successfully': (r) => r.status === 201,
  });
  
  if (!createSuccess) {
    // Display details for non-200 series responses
    if (createRes.status < 200 || createRes.status >= 300) {
      const statusText = createRes.status === 422 ? "Validation Error" :
                         createRes.status >= 500 ? "Server Error" :
                         createRes.status >= 400 ? "Client Error" : "Unknown Error";
                        
      console.error(`SubnetPoolClaim creation failed (${createRes.status} ${statusText})`);
      try {
        const errorDetails = JSON.parse(createRes.body);
        console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
        
        // Detailed display of validation error causes
        if (errorDetails.details && errorDetails.details.causes) {
          console.error(`Validation causes: ${JSON.stringify(errorDetails.details.causes, null, 2)}`);
        } else if (errorDetails.message) {
          console.error(`Error message: ${errorDetails.message}`);
        }
        
        // Also display the sent payload to make problem identification easier
        console.error(`Sent payload: ${createPayload}`);
      } catch (e) {
        console.error(`Error response parsing failed: ${e.message}, raw body: ${createRes.body}`);
      }
    }
    
    // Back off for 5xx series errors
    if (createRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0; // Reset
      }
    }
    return;
  } else {
    consecutiveErrors = 0; // Reset on success
  }

  // Wait 500ms - for resource to be created and subnet to be assigned
  sleep(0.5);
  
  // Read
  let getUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpoolclaims/${claimName}`;
  let getRes = http.get(getUrl, commonParams);
  
  const getSuccess = check(getRes, {
    'PoolClaim read successfully': (r) => r.status === 200,
  });
  
  if (!getSuccess && getRes.status >= 500) {
    consecutiveErrors++;
    if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
      sleep(ERROR_BACKOFF_SECONDS);
      consecutiveErrors = 0;
    }
    return;
  } else if (getSuccess) {
    consecutiveErrors = 0;
  }
  
  // Check if claim has a CIDR assigned (status field)
  if (getRes.status === 200) {
    try {
      const claim = JSON.parse(getRes.body);
      check(claim, {
        'PoolClaim has status': (obj) => obj.status !== undefined,
        'PoolClaim has assigned CIDR': (obj) =>
          obj.status &&
          obj.status.phase === 'Bound' &&
          obj.status.subnetRef !== undefined,
      });
    } catch (e) {
      console.error(`Error parsing pool claim response: ${e.message}`);
    }
  }
  
  // Wait a bit before updating
  sleep(0.3);
  
  // Update - add labels
  try {
    const existingClaim = JSON.parse(getRes.body);
    
    // Add Labels
    if (!existingClaim.metadata.labels) {
      existingClaim.metadata.labels = {};
    }
    existingClaim.metadata.labels['load-test'] = 'true';
    existingClaim.metadata.labels['updated-at'] = new Date().toISOString();
    
    let updateUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpoolclaims/${claimName}`;
    let updateRes = http.put(updateUrl, JSON.stringify(existingClaim), commonParams);
    
    const updateSuccess = check(updateRes, {
      'PoolClaim updated successfully': (r) => r.status === 200,
    });
    
    if (!updateSuccess && updateRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    } else if (updateSuccess) {
      consecutiveErrors = 0;
    }
  } catch (e) {
    console.error(`Error updating pool claim: ${e.message}`);
    console.error(`Update failed. Payload was: ${JSON.stringify(existingClaim, null, 2)}`);
  }
  
  // Wait a bit before deleting
  sleep(0.5);
  
  // Delete - deleting the claim also deletes associated subnet
  let deleteUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpoolclaims/${claimName}`;
  let deleteRes = http.del(deleteUrl, null, commonParams);
  
  const deleteSuccess = check(deleteRes, {
    'PoolClaim deleted successfully': (r) => r.status === 200,
  });
  
  if (!deleteSuccess) {
    // Display details for non-200 series responses
    if (deleteRes.status < 200 || deleteRes.status >= 300) {
      console.error(`SubnetPoolClaim deletion failed (${deleteRes.status}): ${deleteRes.body}`);
      try {
        const errorDetails = JSON.parse(deleteRes.body);
        console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
      } catch (e) {
        // Don't error on parse failure
      }
    }
    
    if (deleteRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    }
  } else if (deleteSuccess) {
    consecutiveErrors = 0;
  }
  
  // Wait a bit before the next iteration (wait for subnet to be fully deleted)
  sleep(1.5);
}

// End of test processing
export function teardown(data) {
  const endTime = new Date().toISOString();
  console.log(`Test completed. Started: ${data.startTime}, Ended: ${endTime}`);
  
  // Keep the test pool
}