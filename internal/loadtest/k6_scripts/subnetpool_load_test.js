import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

// Test configuration
export const options = {
  scenarios: {
    pool_ops: {
      executor: 'ramping-arrival-rate',
      // Gradually increasing RPS profile
      startRate: 1,  // Initial requests per second
      timeUnit: '1s',
      // Different stage settings for SMOKE/SOAK modes
      stages: __ENV.SOAK === 'true' ? [
        // SOAK: Slowly increase to target RPS over 10 minutes, then maintain
        { duration: '10m', target: Number(__ENV.MAX_RPS_POOL || 2) },
        { duration: __ENV.DURATION || '30m', target: Number(__ENV.MAX_RPS_POOL || 2) }
      ] : [
        // SMOKE: Increase to target RPS over 60 seconds, maintain for remaining time
        { duration: '60s', target: Number(__ENV.MAX_RPS_POOL || 1) },
        { duration: __ENV.DURATION || '5m', target: Number(__ENV.MAX_RPS_POOL || 1) }
      ],
      preAllocatedVUs: __ENV.SOAK === 'true' ? 5 : 3,
      maxVUs: __ENV.SOAK === 'true' ? 50 : 20,
      gracefulStop: '1m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'], // Allow error rate less than 1%
    http_req_duration: ['p(95)<3000'], // 95th percentile under 3 seconds
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
const PARENT_POOL_NAME = __ENV.PARENT_POOL_NAME || 'parent-pool-for-load';

// Headers
const headers = {
  'Content-Type': 'application/json',
};

// When running in Kubernetes with k6-operator,
// authentication credentials for the Kubernetes API server are needed
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

// Create parent pool (if it doesn't exist)
function ensureParentPool() {
  const poolManifest = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: PARENT_POOL_NAME,
      namespace: NAMESPACE,
    },
    spec: {
      cidr: '10.0.0.0/8',
      defaultBlockSize: 16, // Parent pool is large
    }
  });

  const url = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools`;

  // Check if pool exists with GET
  const checkRes = http.get(`${url}/${PARENT_POOL_NAME}`, commonParams);
  if (checkRes.status === 404) {
    console.log(`Creating parent pool ${PARENT_POOL_NAME}`);
    const createRes = http.post(url, poolManifest, commonParams);
    check(createRes, {
      'Parent pool created': (r) => r.status === 201 || r.status === 200,
    });
    sleep(2); // Wait for parent pool to be created
  } else {
    console.log(`Parent pool ${PARENT_POOL_NAME} already exists`);
  }
}

// Verify parent pool at test start
export function setup() {
  console.log(`Starting SubnetPool CRUD test against ${API_BASE}`);
  ensureParentPool();
  return { startTime: new Date().toISOString() };
}

// Main test function
export default function() {
  const poolName = `load-test-pool-${randomString(6)}`.toLowerCase();
  
  // Create - create child pool
  const createPayload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: poolName,
      namespace: NAMESPACE,
    },
    spec: {
      // Specify parent pool to create as a subpool (optional, creates standalone if not specified)
      cidr: '192.168.0.0/20', // Use CIDR outside parent pool range
      defaultBlockSize: 24,
      minBlockSize: 24,
      maxBlockSize: 28,
      strategy: "Linear"
    }
  });
  
  let createUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools`;
  let createRes = http.post(createUrl, createPayload, commonParams);
  
  // Check request result - count and process errors
  const createSuccess = check(createRes, {
    'SubnetPool created successfully': (r) => r.status === 201,
  });
  
  if (!createSuccess) {
    // Display details for non-200 series responses
    if (createRes.status < 200 || createRes.status >= 300) {
      const statusText = createRes.status === 422 ? "Validation Error" :
                         createRes.status >= 500 ? "Server Error" :
                         createRes.status >= 400 ? "Client Error" : "Unknown Error";
                        
      console.error(`SubnetPool creation failed (${createRes.status} ${statusText})`);
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

  // Wait 200ms - for resource to be created
  sleep(0.2);
  
  // Read
  let getUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools/${poolName}`;
  let getRes = http.get(getUrl, commonParams);
  
  const getSuccess = check(getRes, {
    'SubnetPool read successfully': (r) => r.status === 200,
  });
  
  if (!getSuccess) {
    // Display details for non-200 series responses
    if (getRes.status < 200 || getRes.status >= 300) {
      console.error(`SubnetPool retrieval failed (${getRes.status}): ${getRes.body}`);
      try {
        const errorDetails = JSON.parse(getRes.body);
        console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
      } catch (e) {
        // Don't error on parse failure
      }
    }
    
    if (getRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    }
  } else if (getSuccess) {
    consecutiveErrors = 0;
  }
  
  // Wait a bit before updating
  sleep(0.3);
  
  // Update - add labels
  try {
    const existingPool = JSON.parse(getRes.body);
    
    // Add Labels
    if (!existingPool.metadata.labels) {
      existingPool.metadata.labels = {};
    }
    existingPool.metadata.labels['load-test'] = 'true';
    existingPool.metadata.labels['updated-at'] = new Date().toISOString();
    
    let updateUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools/${poolName}`;
    let updateRes = http.put(updateUrl, JSON.stringify(existingPool), commonParams);
    
    const updateSuccess = check(updateRes, {
      'SubnetPool updated successfully': (r) => r.status === 200,
    });
    
    if (!updateSuccess) {
      // Display details for non-200 series responses
      if (updateRes.status < 200 || updateRes.status >= 300) {
        console.error(`SubnetPool update failed (${updateRes.status}): ${updateRes.body}`);
        try {
          const errorDetails = JSON.parse(updateRes.body);
          console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
        } catch (e) {
          // Don't error on parse failure
        }
      }
      
      if (updateRes.status >= 500) {
        consecutiveErrors++;
        if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
          console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
          sleep(ERROR_BACKOFF_SECONDS);
          consecutiveErrors = 0;
        }
      }
    } else if (updateSuccess) {
      consecutiveErrors = 0;
    }
  } catch (e) {
    console.error(`Error updating subnet pool: ${e.message}`);
    console.error(`Update failed. Payload was: ${JSON.stringify(existingPool, null, 2)}`);
  }
  
  // Wait a bit before deleting
  sleep(0.5);
  
  // Delete
  let deleteUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools/${poolName}`;
  let deleteRes = http.del(deleteUrl, null, commonParams);
  
  const deleteSuccess = check(deleteRes, {
    'SubnetPool deleted successfully': (r) => r.status === 200,
  });
  
  if (!deleteSuccess) {
    // Display details for non-200 series responses
    if (deleteRes.status < 200 || deleteRes.status >= 300) {
      console.error(`SubnetPool deletion failed (${deleteRes.status}): ${deleteRes.body}`);
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
  
  // Wait a bit before the next iteration
  sleep(1.0);
}

// End of test processing
export function teardown(data) {
  const endTime = new Date().toISOString();
  console.log(`Test completed. Started: ${data.startTime}, Ended: ${endTime}`);
  
  // Keep the parent pool for reuse in CI environment
}