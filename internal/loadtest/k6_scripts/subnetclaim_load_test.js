import http from 'k6/http';
import { check, sleep } from 'k6';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

// Test configuration
export const options = {
  scenarios: {
    subnetclaim_ops: {
      executor: 'ramping-arrival-rate',
      // Gradually increasing RPS profile
      startRate: 3,  // Initial requests per second
      timeUnit: '1s',
      // Different stage settings for SMOKE/SOAK modes
      stages: __ENV.SOAK === 'true' ? [
        // SOAK: Slowly increase to target RPS over 10 minutes, then maintain
        { duration: '10m', target: Number(__ENV.MAX_RPS_CLAIM || 10) },
        { duration: __ENV.DURATION || '30m', target: Number(__ENV.MAX_RPS_CLAIM || 10) }
      ] : [
        // SMOKE: Increase to target RPS over 60 seconds, maintain for remaining time
        { duration: '60s', target: Number(__ENV.MAX_RPS_CLAIM || 5) },
        { duration: __ENV.DURATION || '5m', target: Number(__ENV.MAX_RPS_CLAIM || 5) }
      ],
      preAllocatedVUs: __ENV.SOAK === 'true' ? 10 : 5,
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

// Function to generate random CIDR (for /16 pool)
function randomPoolCIDR() {
  // Generate random CIDR in the format 10.0-255.0-255.0/16
  const octet2 = Math.floor(Math.random() * 256);
  const octet3 = Math.floor(Math.random() * 256);
  return `10.${octet2}.${octet3}.0/16`;
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

// Setup function at test start
export function setup() {
  console.log(`Starting SubnetClaim with Pool creation test against ${API_BASE}`);
  return { startTime: new Date().toISOString() };
}

// Main test function - for each execution, perform "Pool creation→Claim→Verification→Deletion→Re-Claim"
export default function() {
  const uniqueId = randomString(6).toLowerCase();
  const poolName = `loadtest-pool-${uniqueId}`;
  const claimName = `claim-${uniqueId}`;
  const clusterId = `cluster-${uniqueId}`;
  const cidrPrefix = "24"; // Request /24 subnet
  
  // 1. Create SubnetPool
  const poolCIDR = randomPoolCIDR(); // /16 CIDR
  const poolPayload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: poolName,
      namespace: NAMESPACE,
      labels: {
        'loadtest': 'true',
        'created-by': 'k6-loadtest'
      }
    },
    spec: {
      cidr: poolCIDR,
      defaultBlockSize: 24,
      strategy: "Linear" // Default allocation strategy
    }
  });
  
  // Pool creation API call
  const createPoolUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetpools`;
  const createPoolRes = http.post(createPoolUrl, poolPayload, commonParams);
  
  const poolSuccess = check(createPoolRes, {
    'SubnetPool created successfully': (r) => r.status === 201,
  });
  
  if (!poolSuccess) {
    console.error(`Failed to create SubnetPool: ${createPoolRes.status} ${createPoolRes.body}`);
    // For 5xx series errors, increase consecutive error count
    if (createPoolRes.status >= 500) {
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
  
  // After Pool creation, wait a bit for resource initialization
  sleep(0.5);
  
  // 2. Create SubnetClaim
  const createClaimPayload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetClaim',
    metadata: {
      name: claimName,
      namespace: NAMESPACE,
      labels: {
        'loadtest': 'true',
        'created-by': 'k6-loadtest'
      }
    },
    spec: {
      poolRef: poolName,
      clusterID: clusterId,
      blockSize: parseInt(cidrPrefix),   // Changed prefixLength to blockSize
    }
  });
  
  // Claim creation API call
  const createClaimUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetclaims`;
  const createClaimRes = http.post(createClaimUrl, createClaimPayload, commonParams);
  
  const claimSuccess = check(createClaimRes, {
    'SubnetClaim created successfully': (r) => r.status === 201,
  });
  
  if (!claimSuccess) {
    // Display details for non-200 series responses
    if (createClaimRes.status < 200 || createClaimRes.status >= 300) {
      const statusText = createClaimRes.status === 422 ? "Validation Error" :
                        createClaimRes.status >= 500 ? "Server Error" :
                        createClaimRes.status >= 400 ? "Client Error" : "Unknown Error";
                        
      console.error(`SubnetClaim creation failed (${createClaimRes.status} ${statusText})`);
      try {
        const errorDetails = JSON.parse(createClaimRes.body);
        console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
        
        // Detailed display of validation error causes (avoid using optional chaining)
        if (errorDetails.details && errorDetails.details.causes) {
          console.error(`Validation causes: ${JSON.stringify(errorDetails.details.causes, null, 2)}`);
        } else if (errorDetails.message) {
          console.error(`Error message: ${errorDetails.message}`);
        }
        
        // Also display the sent payload to make problem identification easier
        console.error(`Sent payload: ${createClaimPayload}`);
      } catch (e) {
        console.error(`Error response parsing failed: ${e.message}, raw body: ${createClaimRes.body}`);
      }
    }
    
    // Back off for 5xx series errors
    if (createClaimRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    }
    return;
  } else {
    consecutiveErrors = 0;
  }
  
  // 3. Verify Subnet is created (wait up to 3 seconds)
  const readClaimUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnetclaims/${claimName}`;
  let subnetName = null;
  let subnetFound = false;
  let maxTries = 6; // Maximum 6 attempts (every 0.5 seconds)
  
  for (let i = 0; i < maxTries; i++) {
    const readClaimRes = http.get(readClaimUrl, commonParams);
    
    if (readClaimRes.status === 200) {
      try {
        const claim = JSON.parse(readClaimRes.body);
        
        // Get Subnet name from SubnetClaim status
        if (claim.status && claim.status.subnetRef) {
          subnetName = claim.status.subnetRef;
          subnetFound = true;
          break;
        }
      } catch (e) {
        console.error(`Error parsing SubnetClaim: ${e.message}`);
      }
    } else if (readClaimRes.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    }
    
    sleep(0.5); // Wait 0.5 seconds and retry
  }
  
  const claimCheckSuccess = check(null, {
    'Subnet was created from SubnetClaim': () => subnetFound,
  });
  
  // Exit if Subnet not found
  if (!subnetFound) {
    console.error(`Subnet was not created from SubnetClaim ${claimName} after ${maxTries * 0.5}s`);
    return;
  } else {
    consecutiveErrors = 0; // Reset on success
  }
  
  // 4. Verify the created Subnet
  const readSubnetUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets/${subnetName}`;
  const readSubnetRes = http.get(readSubnetUrl, commonParams);
  
  const subnetSuccess = check(readSubnetRes, {
    'Subnet read successfully': (r) => r.status === 200,
    'Subnet has CIDR allocated': (r) => {
      try {
        const subnet = JSON.parse(r.body);
        return subnet.status && subnet.status.phase === 'Allocated';
      } catch (e) {
        return false;
      }
    },
  });
  
  if (!subnetSuccess && readSubnetRes.status >= 500) {
    consecutiveErrors++;
    if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
      sleep(ERROR_BACKOFF_SECONDS);
      consecutiveErrors = 0;
    }
  } else if (subnetSuccess) {
    consecutiveErrors = 0;
  }
  
  // 5. Delete Subnet
  const deleteSubnetUrl = `${API_BASE}/apis/plexaubnet.io/v1alpha1/namespaces/${NAMESPACE}/subnets/${subnetName}`;
  const deleteSubnetRes = http.del(deleteSubnetUrl, null, commonParams);
  
  const deleteSuccess = check(deleteSubnetRes, {
    'Subnet deleted successfully': (r) => r.status === 200,
  });
  
  if (!deleteSuccess && deleteSubnetRes.status >= 500) {
    consecutiveErrors++;
    if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
      sleep(ERROR_BACKOFF_SECONDS);
      consecutiveErrors = 0;
    }
  } else if (deleteSuccess) {
    consecutiveErrors = 0;
  }
  
  // Wait a bit after deletion
  sleep(0.5);
  
  // 6. Create another SubnetClaim with the same Pool (2nd attempt)
  const claim2Name = `claim2-${uniqueId}`;
  const createClaim2Payload = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetClaim',
    metadata: {
      name: claim2Name,
      namespace: NAMESPACE,
      labels: {
        'loadtest': 'true',
        'created-by': 'k6-loadtest'
      }
    },
    spec: {
      poolRef: poolName,
      clusterID: `${clusterId}-2`,
      blockSize: parseInt(cidrPrefix),  // Changed prefixLength to blockSize
    }
  });
  
  const createClaim2Res = http.post(createClaimUrl, createClaim2Payload, commonParams);
  
  const claim2Success = check(createClaim2Res, {
    'Second SubnetClaim created successfully': (r) => r.status === 201,
  });
  
  if (!claim2Success) {
    // Display details for non-200 series responses
    if (createClaim2Res.status < 200 || createClaim2Res.status >= 300) {
      const statusText = createClaim2Res.status === 422 ? "Validation Error" :
                        createClaim2Res.status >= 500 ? "Server Error" :
                        createClaim2Res.status >= 400 ? "Client Error" : "Unknown Error";
                        
      console.error(`Second SubnetClaim creation failed (${createClaim2Res.status} ${statusText})`);
      try {
        const errorDetails = JSON.parse(createClaim2Res.body);
        console.error(`Error details: ${JSON.stringify(errorDetails, null, 2)}`);
        
        if (errorDetails.details && errorDetails.details.causes) {
          console.error(`Validation causes: ${JSON.stringify(errorDetails.details.causes, null, 2)}`);
        } else if (errorDetails.message) {
          console.error(`Error message: ${errorDetails.message}`);
        }
        
        console.error(`Sent payload: ${createClaim2Payload}`);
      } catch (e) {
        console.error(`Error response parsing failed: ${e.message}, raw body: ${createClaim2Res.body}`);
      }
    }
    
    // Back off for 5xx series errors
    if (createClaim2Res.status >= 500) {
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`${MAX_CONSECUTIVE_ERRORS} consecutive server errors occurred. Backing off for ${ERROR_BACKOFF_SECONDS} seconds`);
        sleep(ERROR_BACKOFF_SECONDS);
        consecutiveErrors = 0;
      }
    }
  } else if (claim2Success) {
    consecutiveErrors = 0;
  }
  
  // Wait a bit before the next iteration
  sleep(1.0);
}

// End of test processing
export function teardown(data) {
  const endTime = new Date().toISOString();
  console.log(`SubnetClaim with Pool creation test completed. Started: ${data.startTime}, Ended: ${endTime}`);
  // Don't delete pools, leave cleanup to garbage collection
}