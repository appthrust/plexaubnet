import { check, sleep } from 'k6';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.1.0/index.js';
import http from 'k6/http';

// Test configuration
export const options = {
  vus: 5,             // 5 parallel users
  iterations: 20,     // Each user performs 20 operations
  thresholds: {
    http_req_failed: ['rate<0.01'], // HTTP failure rate less than 1%
    http_req_duration: ['p(95)<5000'], // 95% of requests complete within 5 seconds
  },
};

// K8s API URL (Access via Service Account token)
const apiHost = __ENV.API_HOST || 'kubernetes.default.svc';
const namespace = __ENV.NAMESPACE || 'default';
const apiBase = `https://${apiHost}/apis/plexaubnet.io/v1alpha1/namespaces/${namespace}`;

// Auth headers
const token = open('/var/run/secrets/kubernetes.io/serviceaccount/token');
const headers = {
  'Content-Type': 'application/json',
  'Authorization': `Bearer ${token}`,
};

// CA certificate (for TLS verification)
const certificate = open('/var/run/secrets/kubernetes.io/serviceaccount/ca.crt');

// Default request configuration
const requestConfig = {
  headers: headers,
  timeout: '30s', // Timeout setting
};

// SubnetPool creation function
function createPool() {
  const poolId = `notfound-test-${randomString(6)}`;
  
  const poolData = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'SubnetPool',
    metadata: {
      name: poolId,
      labels: {
        'test-type': 'notfound-test',
      },
    },
    spec: {
      cidr: `10.${Math.floor(Math.random() * 250)}.0.0/16`,
      defaultBlockSize: 24,
      strategy: 'Linear',
    },
  });

  const poolResponse = http.post(
    `${apiBase}/subnetpools`,
    poolData,
    requestConfig
  );

  check(poolResponse, {
    'Pool created': (r) => r.status === 201,
  });

  return poolId;
}

// Subnet creation function
function createSubnet(poolId, idx) {
  const subnetId = `notfound-subnet-${randomString(6)}`;
  const octets = Math.floor(Math.random() * 250);

  const subnetData = JSON.stringify({
    apiVersion: 'plexaubnet.io/v1alpha1',
    kind: 'Subnet',
    metadata: {
      name: subnetId,
      labels: {
        'test-type': 'notfound-test',
      },
    },
    spec: {
      cidr: `10.${octets}.${idx}.0/24`,
      poolRef: poolId,
      clusterID: `cluster-${idx}`,
    },
  });

  const subnetResponse = http.post(
    `${apiBase}/subnets`,
    subnetData,
    requestConfig
  );

  return { id: subnetId, status: subnetResponse.status === 201 };
}

// Subnet deletion function
function deleteSubnet(subnetId) {
  const deleteResponse = http.del(
    `${apiBase}/subnets/${subnetId}`,
    null,
    requestConfig
  );

  return deleteResponse.status === 200;
}

// Main execution function
export default function() {
  // 1. Create SubnetPool
  const poolId = createPool();
  
  // 2. Create 20 Subnets → Immediately delete them rapidly
  const subnets = [];
  for (let i = 0; i < 20; i++) {
    const subnet = createSubnet(poolId, i);
    if (subnet.status) {
      subnets.push(subnet.id);
    }
  }

  // 3. Short delay (controller starts Reconcile but doesn't complete)
  sleep(0.05); // 50ms

  // 4. Delete all Subnets
  let successCount = 0;
  for (const subnetId of subnets) {
    if (deleteSubnet(subnetId)) {
      successCount++;
    }
  }

  // 5. Check success rate
  check(null, {
    'Subnets successfully deleted': () => successCount === subnets.length,
  });
  
  // 6. Wait a bit for the remaining test time (interval until next iteration)
  sleep(0.5);
}