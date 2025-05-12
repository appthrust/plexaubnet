import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

// Test configuration
export const options = {
  scenarios: {
    subnet_ops: {
      executor: 'ramping-arrival-rate',
      // Gradually increasing RPS profile
      startRate: 5,  // Initial requests per second
      timeUnit: '1s',
      // Different stage settings for SMOKE/SOAK modes
      stages: __ENV.SOAK === 'true' ? [
        // SOAK: Slowly increase to target RPS over 10 minutes, then maintain
        { duration: '10m', target: Number(__ENV.MAX_RPS_SUBNET || 25) },
        { duration: __ENV.DURATION || '30m', target: Number(__ENV.MAX_RPS_SUBNET || 25) }
      ] : [
        // SMOKE: Increase to target RPS over 60 seconds, maintain for remaining time
        { duration: '60s', target: Number(__ENV.MAX_RPS_SUBNET || 25) },
        { duration: __ENV.DURATION || '5m', target: Number(__ENV.MAX_RPS_SUBNET || 25) }
      ],
      preAllocatedVUs: __ENV.SOAK === 'true' ? 10 : 5,
      maxVUs: __ENV.SOAK === 'true' ? 200 : 50,
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
const POOL_NAME = __ENV.POOL_NAME || 'test-pool-for-load';

// Function to generate random CIDR (/24 subnet)
function randomCIDR() {
  // Generate random CIDR in the format 10.0-255.0-255.0/24
  const octet2 = Math.floor(Math.random() * 256);
  const octet3 = Math.floor(Math.random() * 256);
  return `10.${octet2}.${octet3}.0/24`;
}

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

// Use existing SubnetPool (expected to be created in advance)
function ensureSubnetPool() {
  const poolManifest = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: POOL_NAME,
      namespace: NAMESPACE,
    },
    spec: {
      cidr: '10.0.0.0/16',
      defaultBlockSize: 24,
    }
  });

  const url = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools`;

  // Try PUT for Upsert operation (create if not exists, update if exists)
  const checkRes = http.get(`${url}/${POOL_NAME}`, commonParams);
  if (checkRes.status === 404) {
    console.log(`Creating subnet pool ${POOL_NAME}`);
    const createRes = http.post(url, poolManifest, commonParams);
    check(createRes, {
      'Pool created': (r) => r.status === 201 || r.status === 200,
    });
  } else {
    console.log(`Subnet pool ${POOL_NAME} already exists`);
  }
}

// Verify SubnetPool at test start
export function setup() {
  console.log(`Starting Subnet CRUD test against ${API_BASE}`);
  ensureSubnetPool();
  return { startTime: new Date().toISOString() };
}

// Main test function
export default function() {
  const subnetName = `load-test-subnet-${randomString(6)}`.toLowerCase();
  const clusterId = `load-test-cluster-${randomString(8)}`.toLowerCase();
  
  // Create
  const createPayload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'Subnet',
    metadata: {
      name: subnetName,
      namespace: NAMESPACE,
    },
    spec: {
      poolRef: POOL_NAME,
      clusterID: clusterId,
      cidr: randomCIDR(), // Required field - to avoid validation error
    }
  });
  
  let createUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets`;
  let createRes = http.post(createUrl, createPayload, commonParams);
  
  // Check request result - count and process errors
  const createSuccess = check(createRes, {
    'Subnet created successfully': (r) => r.status === 201,
  });
  
  if (!createSuccess) {
    // Display details for non-200 series responses
    if (createRes.status < 200 || createRes.status >= 300) {
      const statusText = createRes.status === 422 ? "Validation Error" :
                         createRes.status >= 500 ? "Server Error" :
                         createRes.status >= 400 ? "Client Error" : "Unknown Error";
                        
      console.error(`Subnet creation failed (${createRes.status} ${statusText})`);
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
  let getUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets/${subnetName}`;
  let getRes = http.get(getUrl, commonParams);
  
  const getSuccess = check(getRes, {
    'Subnet read successfully': (r) => r.status === 200,
    'Subnet has CIDR allocated': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.status && body.status.phase === 'Allocated';
      } catch (e) {
        return false;
      }
    },
  });
  
  if (!getSuccess && getRes.status >= 500) {
    consecutiveErrors++;
    if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
      sleep(ERROR_BACKOFF_SECONDS);
      consecutiveErrors = 0;
    }
  } else if (getSuccess) {
    consecutiveErrors = 0;
  }
  
  // Wait a bit before updating
  sleep(0.3);
  
  // Update - add labels
  try {
    const existingSubnet = JSON.parse(getRes.body);
    
    // Add Labels
    if (!existingSubnet.metadata.labels) {
      existingSubnet.metadata.labels = {};
    }
    existingSubnet.metadata.labels['load-test'] = 'true';
    existingSubnet.metadata.labels['updated-at'] = new Date().toISOString();
    
    let updateUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets/${subnetName}`;
    let updateRes = http.put(updateUrl, JSON.stringify(existingSubnet), commonParams);
    
    const updateSuccess = check(updateRes, {
      'Subnet updated successfully': (r) => r.status === 200,
    });
    
    if (!updateSuccess) {
      // Display details for non-200 series responses
      if (updateRes.status < 200 || updateRes.status >= 300) {
        console.error(`Subnet update failed (${updateRes.status}): ${updateRes.body}`);
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
    console.error(`Error updating subnet: ${e.message}`);
  }
  
  // Wait a bit before deleting
  sleep(0.5);
  
  // Delete
  let deleteUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets/${subnetName}`;
  let deleteRes = http.del(deleteUrl, null, commonParams);
  
  const deleteSuccess = check(deleteRes, {
    'Subnet deleted successfully': (r) => r.status === 200,
  });
  
  if (!deleteSuccess) {
    // Display details for non-200 series responses
    if (deleteRes.status < 200 || deleteRes.status >= 300) {
      console.error(`Subnet deletion failed (${deleteRes.status}): ${deleteRes.body}`);
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
  
  // Keep the Pool for reuse in CI
}