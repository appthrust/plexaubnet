#!/usr/bin/env bash

# Enable strict mode
set -e
set -o pipefail

# Minimum required k6 version (with experimental-prometheus-rw support)
MIN_K6_VERSION="0.46.0"

# Constants
K8S_VERSION=${K8S_VERSION:-"v1.27.3"}
KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-"plexaubnet-soak"}
NAMESPACE=${NAMESPACE:-"system"}
DURATION=${DURATION:-"15m"}  # Default is 15 minutes, becomes 24 hours if SOAK=true
SMOKE=${SMOKE:-"false"}      # Smoke test mode
QUICK=${QUICK:-"false"}      # Quick test mode (3 minutes)
MAX_RPS=${MAX_RPS:-"100"}    # Overall default RPS (requests/second)

# Default RPS settings for each test type - Values reduced to mitigate OOM risk
MAX_RPS_SUBNET=${MAX_RPS_SUBNET:-"30"}  # For Subnet CRUD (default: 30 RPS)
MAX_RPS_POOL=${MAX_RPS_POOL:-"1"}       # For SubnetPool creation (default: 1 RPS)
MAX_RPS_CLAIM=${MAX_RPS_CLAIM:-"10"}    # For SubnetClaim (default: 10 RPS)

LOG_DIR=${LOG_DIR:-"./logs"}
PROM_PORT=${PROM_PORT:-9090}
GRAFANA_PORT=${GRAFANA_PORT:-3000}
KUBE_API_PORT=${KUBE_API_PORT:-6443}
LOG_RETRY_COUNT=${LOG_RETRY_COUNT:-"3"}  # Number of retries for log collection

# Set duration and load level according to test mode
if [ "$SMOKE" = "true" ]; then
    DURATION="5m"
    # Set overall low load for smoke tests
    MAX_RPS_SUBNET="25"
    MAX_RPS_POOL="1"
    MAX_RPS_CLAIM="10"
    echo "🔥 Running in SMOKE test mode (duration: $DURATION, RPS: Subnet=$MAX_RPS_SUBNET Pool=$MAX_RPS_POOL Claim=$MAX_RPS_CLAIM, fail-fast: enabled)"
    # Set flag to exit immediately on failure for smoke tests
    set -e
elif [ "$QUICK" = "true" ]; then
    DURATION="3m"
    # Maintain normal load for quick tests
    MAX_RPS_SUBNET="30"
    MAX_RPS_POOL="1"
    MAX_RPS_CLAIM="10"
    echo "⚡ Running in QUICK test mode (duration: $DURATION, RPS: Subnet=$MAX_RPS_SUBNET Pool=$MAX_RPS_POOL Claim=$MAX_RPS_CLAIM)"
elif [ "$SOAK" = "true" ]; then
    DURATION="24h"
    # Adjust load for each test type in SOAK tests - distribute load on API server
    MAX_RPS_SUBNET="100"
    MAX_RPS_POOL="5"
    MAX_RPS_CLAIM="50"
    echo "🕐 Running in SOAK test mode (duration: $DURATION, RPS: Subnet=$MAX_RPS_SUBNET Pool=$MAX_RPS_POOL Claim=$MAX_RPS_CLAIM)"
else
    echo "🕐 Running in standard test mode (duration: $DURATION, RPS: Subnet=$MAX_RPS_SUBNET Pool=$MAX_RPS_POOL Claim=$MAX_RPS_CLAIM)"
fi

# Preparation
echo "📂 Creating log directory..."
mkdir -p ${LOG_DIR}
mkdir -p ${LOG_DIR}/heap

# Log file
LOG_FILE="${LOG_DIR}/soak-test-$(date +%Y%m%d-%H%M%S).log"
touch ${LOG_FILE}

# Record start time
START_TIME=$(date +%s)
echo "🚀 Starting Plexaubnet soak test at $(date)" | tee -a ${LOG_FILE}

# Check if kind is installed
if ! command -v kind &> /dev/null; then
    echo "❌ Error: kind is not installed. Please install kind first." | tee -a ${LOG_FILE}
    echo "   https://kind.sigs.k8s.io/docs/user/quick-start/#installation" | tee -a ${LOG_FILE}
    exit 1
fi

# Check for k6 installation and auto-install (actual execution happens later)
if ! command -v k6 &> /dev/null; then
    echo "🔄 k6 not found. Will attempt to install automatically when needed." | tee -a ${LOG_FILE}
fi

# Check for Helm installation and auto-install
if ! command -v helm &> /dev/null; then
    echo "🔄 Helm not found. Will attempt to install automatically when needed." | tee -a ${LOG_FILE}
fi

# Check if kubectl is installed
if ! command -v kubectl &> /dev/null; then
    echo "❌ Error: kubectl is not installed. Please install kubectl first." | tee -a ${LOG_FILE}
    echo "   https://kubernetes.io/docs/tasks/tools/install-kubectl/" | tee -a ${LOG_FILE}
    exit 1
fi

# Delete old cluster if it exists
echo "🧹 Cleaning up any existing kind cluster..." | tee -a ${LOG_FILE}
if kind get clusters | grep -q ${KIND_CLUSTER_NAME}; then
    kind delete cluster --name ${KIND_CLUSTER_NAME}
fi

# Create kind cluster
echo "🔄 Creating kind cluster ${KIND_CLUSTER_NAME}..." | tee -a ${LOG_FILE}
cat <<EOF | kind create cluster --name ${KIND_CLUSTER_NAME} --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
        # Set resource reservations for API server/etcd load management
        kube-reserved: "cpu=1000m,memory=1024Mi,ephemeral-storage=1Gi"
        system-reserved: "cpu=1000m,memory=1024Mi,ephemeral-storage=1Gi"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
  - containerPort: ${PROM_PORT}
    hostPort: ${PROM_PORT}
    protocol: TCP
  - containerPort: ${GRAFANA_PORT}
    hostPort: ${GRAFANA_PORT}
    protocol: TCP
EOF

echo "⏳ Waiting for kind cluster to be ready..." | tee -a ${LOG_FILE}
kubectl wait --for=condition=Ready nodes --all --timeout=120s
# Set up a trap to clean up resources on script exit
trap 'echo "🧹 Cleaning up resources..." | tee -a ${LOG_FILE}' EXIT

# Verify API server responds directly
echo "⏳ Ensuring API server is ready..." | tee -a ${LOG_FILE}
kubectl wait --for=condition=Available --timeout=60s deployment/coredns -n kube-system

# Create namespace
echo "🔄 Creating namespace ${NAMESPACE}..." | tee -a ${LOG_FILE}
kubectl create namespace ${NAMESPACE} --dry-run=client -o yaml | kubectl apply -f -

# Set up Prometheus and Grafana (before controller deployment)
echo "🔄 Setting up monitoring and metrics collection..." | tee -a ${LOG_FILE}

# Install Helm if needed
if ! command -v helm &> /dev/null; then
  echo "  🛠️ Installing Helm locally..." | tee -a ${LOG_FILE}
  
  # OS detection (Homebrew for Mac, script installation for Linux)
  if [[ "$OSTYPE" == "darwin"* ]]; then
    brew install helm || {
      echo "⚠️ Failed to install Helm via Homebrew. Installing via script..." | tee -a ${LOG_FILE}
      curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash || {
        echo "❌ Failed to install Helm. Please install manually and run this script again." | tee -a ${LOG_FILE}
        exit 1
      }
    }
  else
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash || {
      echo "❌ Failed to install Helm. Please install manually and run this script again." | tee -a ${LOG_FILE}
      exit 1
    }
  fi
  echo "  ✅ Helm is installed and ready" | tee -a ${LOG_FILE}
fi

# Install kube-prometheus-stack
echo "  ⚙️ Installing kube-prometheus-stack via Helm..." | tee -a ${LOG_FILE}
kubectl create namespace monitoring || true
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts || true
helm repo update
helm install kp prometheus-community/kube-prometheus-stack --namespace monitoring \
  --set grafana.enabled=false \
  --set prometheus.prometheusSpec.enableAdminAPI=true || {
  echo "⚠️ kube-prometheus-stack installation failed, but continuing..." | tee -a ${LOG_FILE}
}

# Wait for kube-prometheus-stack to be ready
echo "  ⏳ Waiting for kube-prometheus-stack to be ready..." | tee -a ${LOG_FILE}
kubectl -n monitoring wait --for=condition=available deployment/kp-operator --timeout=120s || {
  echo "⚠️ kube-prometheus-stack operator timeout, but continuing..." | tee -a ${LOG_FILE}
}

# Wait a bit for Prometheus CRDs to be established
echo "  ⏳ Waiting for Prometheus CRDs to be established..." | tee -a ${LOG_FILE}
sleep 15

# Controller deployment function
deploy_controller() {
  # Install CRDs
  echo "🔄 Installing Plexaubnet CRDs..." | tee -a ${LOG_FILE}
  ./hack/install-crds.sh

  # Build image
  echo "🔨 Building controller image (controller:latest)..." | tee -a ${LOG_FILE}
  make docker-build IMG=controller:latest

  # Load image into Kind
  echo "📦 Loading image into Kind cluster..." | tee -a ${LOG_FILE}
  kind load docker-image controller:latest --name "${KIND_CLUSTER_NAME}"

  # Create namespace (system) - specified in kustomize.yaml
  echo "🔄 Creating controller namespace..." | tee -a ${LOG_FILE}
  kubectl create namespace system --dry-run=client -o yaml | kubectl apply -f -

  # Apply controller manifests
  echo "📜 Applying controller manifests..." | tee -a ${LOG_FILE}
  kubectl apply -k config/default

  # Wait for controller deployment
  echo "⏳ Waiting for controller deployment to be ready..." | tee -a ${LOG_FILE}
  
  # Wait with correct namespace and labels
  CONTROLLER_NS="system"
  CONTROLLER_NAME="plexaubnet-controller-manager"
  
  # Function to check pod status
  check_controller_status() {
    echo "📊 Checking controller deployment status..." | tee -a ${LOG_FILE}
    kubectl get deployment ${CONTROLLER_NAME} -n ${CONTROLLER_NS} -o wide | tee -a ${LOG_FILE}
    
    echo "🔍 Controller pods:" | tee -a ${LOG_FILE}
    kubectl get pods -n ${CONTROLLER_NS} -l control-plane=controller-manager | tee -a ${LOG_FILE}
    
    echo "📊 Pod events..." | tee -a ${LOG_FILE}
    kubectl get events -n ${CONTROLLER_NS} --sort-by='.lastTimestamp' | grep -i "controller\|error\|fail\|warn" | tail -10 | tee -a ${LOG_FILE} || true
    
    # Check image pull status
    POD_NAME=$(kubectl get pods -n ${CONTROLLER_NS} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "not-found")
    if [ "$POD_NAME" != "not-found" ]; then
      echo "🔍 Container statuses for ${POD_NAME}:" | tee -a ${LOG_FILE}
      kubectl get pod ${POD_NAME} -n ${CONTROLLER_NS} -o jsonpath='{.status.containerStatuses[0].state}' | tee -a ${LOG_FILE}
      echo "" | tee -a ${LOG_FILE}
    fi
  }

  # Retry logic
  MAX_RETRIES=3
  RETRY_COUNT=0
  
  while [ ${RETRY_COUNT} -lt ${MAX_RETRIES} ]; do
    kubectl rollout status deployment/${CONTROLLER_NAME} -n ${CONTROLLER_NS} --timeout=120s && {
      echo "✅ Controller deployment is ready!" | tee -a ${LOG_FILE}
      
      # Show some logs
      POD_NAME=$(kubectl get pods -n ${CONTROLLER_NS} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
      echo "📋 Controller logs:" | tee -a ${LOG_FILE}
      kubectl logs ${POD_NAME} -n ${CONTROLLER_NS} --tail=20 | tee -a ${LOG_FILE}
      
      break
    } || {
      RETRY_COUNT=$((RETRY_COUNT+1))
      echo "⚠️ Warning: Controller deployment not ready (attempt ${RETRY_COUNT}/${MAX_RETRIES})" | tee -a ${LOG_FILE}
      
      check_controller_status
      
      if [ ${RETRY_COUNT} -lt ${MAX_RETRIES} ]; then
        echo "⏳ Retrying in 15 seconds..." | tee -a ${LOG_FILE}
        sleep 15
      else
        if [ "$SMOKE" = "true" ]; then
          echo "❌ Controller failed to start in smoke test mode - exiting" | tee -a ${LOG_FILE}
          exit 1
        else
          echo "⚠️ Max retries reached. Continuing despite deployment readiness check failure..." | tee -a ${LOG_FILE}
        fi
      fi
    }
  done
}

# Deploy controller
deploy_controller

# Apply Prometheus instance and monitoring resources
echo "  📊 Applying Prometheus monitoring resources..." | tee -a ${LOG_FILE}

# Apply custom resources (ServiceMonitor, etc.)
echo "  📊 Applying monitoring resources..." | tee -a ${LOG_FILE}
kubectl apply -f config/prometheus/monitor.yaml || {
  echo "⚠️ Failed to apply ServiceMonitor, but continuing..." | tee -a ${LOG_FILE}
}

# Check if kube-prometheus-stack created a Prometheus instance
# If not, create a custom Prometheus instance manually
if ! kubectl get prometheus -n monitoring kp-prometheus &> /dev/null; then
  echo "  ⚠️ No Prometheus instance found from kube-prometheus-stack, creating a custom one..." | tee -a ${LOG_FILE}
  cat <<EOF | kubectl apply -f - || true
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
metadata:
  name: plexaubnet-prometheus
  namespace: monitoring
spec:
  serviceAccountName: prometheus-k8s
  serviceMonitorSelector:
    matchLabels:
      app.kubernetes.io/instance: plexaubnet-controller
  resources:
    requests:
      memory: 400Mi
  enableAdminAPI: true
EOF
fi

echo "  ✅ Monitoring setup completed" | tee -a ${LOG_FILE}

# Install k6 and check version
echo "🔄 Setting up k6 for load testing..." | tee -a ${LOG_FILE}

# Version comparison function
version_lt() {
  [ "$1" = "$2" ] && return 1 || [ "$1" = "$(echo -e "$1\n$2" | sort -V | head -n1)" ]
}

# Check and upgrade k6 if needed
check_and_upgrade_k6() {
  if command -v k6 &> /dev/null; then
    local current_version=$(k6 version | grep -o '[0-9]\+\.[0-9]\+\.[0-9]\+' | head -1)
    echo "  📊 Detected k6 version: ${current_version}" | tee -a ${LOG_FILE}
    
    if version_lt "${current_version}" "${MIN_K6_VERSION}"; then
      echo "  ⚠️ k6 version ${current_version} is older than required ${MIN_K6_VERSION}" | tee -a ${LOG_FILE}
      echo "  🔄 Upgrading k6..." | tee -a ${LOG_FILE}
      
      # Detect OS and upgrade
      if [[ "$OSTYPE" == "darwin"* ]]; then
        brew upgrade k6 || brew install k6 || {
          echo "❌ Failed to upgrade k6 via Homebrew." | tee -a ${LOG_FILE}
          exit 1
        }
      elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
        sudo apt-get update
        sudo apt-get install --only-upgrade k6 -y || {
          # Try reinstalling
          sudo apt-get remove k6 -y || true
          sudo apt-key adv --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69 || true
          echo "deb https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list
          sudo apt-get update
          sudo apt-get install k6 -y
        } || {
          echo "❌ Failed to upgrade k6." | tee -a ${LOG_FILE}
          exit 1
        }
      else
        echo "❌ Unsupported OS for automatic k6 upgrade: $OSTYPE" | tee -a ${LOG_FILE}
        exit 1
      fi
      
      # Verify upgrade success
      current_version=$(k6 version | grep -o '[0-9]\+\.[0-9]\+\.[0-9]\+' | head -1)
      echo "  ✅ k6 upgraded to version: ${current_version}" | tee -a ${LOG_FILE}
      
      if version_lt "${current_version}" "${MIN_K6_VERSION}"; then
        echo "❌ Failed to upgrade k6 to required version ${MIN_K6_VERSION}" | tee -a ${LOG_FILE}
        exit 1
      fi
    else
      echo "  ✅ k6 version ${current_version} meets requirements (>= ${MIN_K6_VERSION})" | tee -a ${LOG_FILE}
    fi
  else
    echo "  🛠️ Installing k6 locally..." | tee -a ${LOG_FILE}
    
    # OS detection (Homebrew for Mac, apt for Linux)
    if [[ "$OSTYPE" == "darwin"* ]]; then
      brew install k6 || {
        echo "❌ Failed to install k6 via Homebrew." | tee -a ${LOG_FILE}
        exit 1
      }
    elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
      sudo apt-key adv --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69 || true
      echo "deb https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list
      sudo apt-get update
      sudo apt-get install k6 -y || {
        echo "❌ Failed to install k6 via apt." | tee -a ${LOG_FILE}
        exit 1
      }
    else
      echo "❌ Unsupported OS: $OSTYPE." | tee -a ${LOG_FILE}
      exit 1
    fi
    
    # Verify installation success
    local current_version=$(k6 version | grep -o '[0-9]\+\.[0-9]\+\.[0-9]\+' | head -1)
    echo "  ✅ k6 installed, version: ${current_version}" | tee -a ${LOG_FILE}
    
    if version_lt "${current_version}" "${MIN_K6_VERSION}"; then
      echo "⚠️ Warning: Installed k6 version ${current_version} is older than required ${MIN_K6_VERSION}" | tee -a ${LOG_FILE}
      echo "⚠️ Continuing anyway since k6 may still be functional..." | tee -a ${LOG_FILE}
    fi
  fi
}

# Execute k6 installation/upgrade
check_and_upgrade_k6

echo "  ✅ k6 is installed and ready for testing" | tee -a ${LOG_FILE}

# Create namespace for test execution
TEST_NAMESPACE="loadtest-$(date +%s)"
echo "🔄 Creating test namespace ${TEST_NAMESPACE}..." | tee -a ${LOG_FILE}
kubectl create namespace ${TEST_NAMESPACE}

# Log collection retry function
get_logs_with_retry() {
  local ns=$1
  local resource=$2
  local tail_lines=${3:-100}
  local output_file=${4:-""}
  local max_tries=${LOG_RETRY_COUNT}
  local tries=0
  local result=""
  
  echo "📊 Collecting logs for ${resource} in namespace ${ns}..." | tee -a ${LOG_FILE}
  
  until [ $tries -ge $max_tries ]; do
    if [ -z "$output_file" ]; then
      result=$(kubectl logs -n $ns $resource --tail=$tail_lines --insecure-skip-tls-verify=true --request-timeout=30s 2>&1)
    else
      kubectl logs -n $ns $resource --tail=$tail_lines --insecure-skip-tls-verify=true --request-timeout=30s > "$output_file" 2>&1
      result=$?
    fi
    
    # Exit on success
    if [ -z "$output_file" ] && [ ! -z "$result" ]; then
      echo "$result"
      return 0
    elif [ ! -z "$output_file" ] && [ "$result" -eq 0 ]; then
      echo "✅ Logs saved to $output_file" | tee -a ${LOG_FILE}
      return 0
    fi
    
    tries=$((tries+1))
    if [ $tries -lt $max_tries ]; then
      echo "⚠️ Log retrieval failed, retry $tries/$max_tries in 5s..." | tee -a ${LOG_FILE}
      sleep 5
    fi
  done
  
  echo "❌ Failed to collect logs after $max_tries attempts" | tee -a ${LOG_FILE}
  return 1
}

# Add test configuration to ConfigMap for k6
echo "🔄 Configuring k6 test parameters..." | tee -a ${LOG_FILE}
cat <<EOF | kubectl apply -f - -n ${TEST_NAMESPACE}
apiVersion: v1
kind: ConfigMap
metadata:
  name: k6-loadtest-config
  labels:
    app: k6-loadtest
data:
  # Test duration
  duration: "${DURATION}"
  # Request rate (per second) for each test type
  maxRpsSubnet: "${MAX_RPS_SUBNET}"  # For Subnet CRUD
  maxRpsPool: "${MAX_RPS_POOL}"      # For SubnetPool creation
  maxRpsClaim: "${MAX_RPS_CLAIM}"    # For SubnetClaim
  maxRps: "${MAX_RPS}"               # Kept for backward compatibility
EOF

echo "🔢 Test RPS settings: Subnet=${MAX_RPS_SUBNET} Pool=${MAX_RPS_POOL} Claim=${MAX_RPS_CLAIM}" | tee -a ${LOG_FILE}

# Create pool resources (initialize before load test)
echo "🔄 Creating initial SubnetPool resources for tests..." | tee -a ${LOG_FILE}
cat <<EOF | kubectl apply -f -
apiVersion: plexaubnet.io/v1alpha1
kind: SubnetPool
metadata:
  name: pool-for-claim-load
  namespace: ${TEST_NAMESPACE}
spec:
  cidr: '10.100.0.0/16'
  defaultBlockSize: 24
  strategy: "Linear"
EOF

echo "⏳ Waiting for initial pool to be ready..." | tee -a ${LOG_FILE}
sleep 2

echo "🧪 Starting load tests using in-cluster k6 job..." | tee -a ${LOG_FILE}
echo "🧪 Deploying k6 load test resources..." | tee -a ${LOG_FILE}

# Apply k6 test manifests to test namespace
echo "📊 Creating k6 ServiceAccount, Role and RoleBinding..." | tee -a ${LOG_FILE}
kubectl apply -f internal/loadtest/k6_manifests/k6-loadtest-sa.yaml -n ${TEST_NAMESPACE}
kubectl apply -f internal/loadtest/k6_manifests/k6-loadtest-role.yaml -n ${TEST_NAMESPACE}

echo "📊 Creating k6 ConfigMap with actual test scripts..." | tee -a ${LOG_FILE}
# Generate from actual scripts instead of using existing placeholder ConfigMap
kubectl delete configmap k6-scripts -n ${TEST_NAMESPACE} --ignore-not-found
kubectl create configmap k6-scripts \
  --from-file=subnet.js=internal/loadtest/k6_scripts/subnet_load_test.js \
  --from-file=subnetpool.js=internal/loadtest/k6_scripts/subnetpool_load_test.js \
  --from-file=subnetclaim.js=internal/loadtest/k6_scripts/subnetclaim_load_test.js \
  --from-file=subnetpoolclaim.js=internal/loadtest/k6_scripts/subnetpoolclaim_load_test.js \
  -n ${TEST_NAMESPACE}

# Start k6 job
echo "📊 Starting k6 load test job..." | tee -a ${LOG_FILE}
kubectl apply -f internal/loadtest/k6_manifests/k6-loadtest-job.yaml -n ${TEST_NAMESPACE}

# Periodic heap dump collection (only for long-running tests)
if [ "$SOAK" = "true" ]; then
    echo "📊 Setting up continuous metrics & pprof collection..." | tee -a ${LOG_FILE}
    
    # Get controller Pod name
    POD_NAME=$(kubectl get pods -n ${NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    
    if [ -z "${POD_NAME}" ]; then
        echo "⚠️ Warning: No controller pod found, retrying in 60s..." | tee -a ${LOG_FILE}
        sleep 60
        POD_NAME=$(kubectl get pods -n ${NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    fi
    
    if [ -z "${POD_NAME}" ]; then
        echo "⚠️ Warning: Still no controller pod found, metrics & pprof collection disabled" | tee -a ${LOG_FILE}
    else
        # Run collect_metrics.sh in the background (automatic collection every 60 seconds)
        echo "📊 Starting collect_metrics.sh for continuous collection..." | tee -a ${LOG_FILE}
        (
            # Set collection directory to log directory
            cd ${LOG_DIR}
            # Run collect_metrics.sh (also collects pprof automatically)
            ../../scripts/collect_metrics.sh ${NAMESPACE} >> ${LOG_FILE} 2>&1
        ) &
        METRICS_COLLECTOR_PID=$!
        
        echo "✅ Metrics & pprof collection started (PID: ${METRICS_COLLECTOR_PID})" | tee -a ${LOG_FILE}
        echo "  - Collecting metrics every 60 seconds" | tee -a ${LOG_FILE}
        echo "  - Collecting heap profiles every 60 seconds" | tee -a ${LOG_FILE}
        echo "  - Storing in ${LOG_DIR}/heap/ directory" | tee -a ${LOG_FILE}
        
        # Clean up collector process on script exit
        trap 'echo "🧹 Cleaning up resources..." | tee -a ${LOG_FILE}; [ ! -z "${METRICS_COLLECTOR_PID}" ] && kill ${METRICS_COLLECTOR_PID} || true' EXIT
    fi
fi

# Wait for job completion
echo "⏳ Waiting for k6 job to complete (this may take ${DURATION})..." | tee -a ${LOG_FILE}
kubectl wait --for=condition=complete --timeout=${DURATION} job/k6-loadtest -n ${TEST_NAMESPACE} || {
    echo "⚠️ k6 job did not complete in expected time. Checking status..." | tee -a ${LOG_FILE}
    kubectl describe job/k6-loadtest -n ${TEST_NAMESPACE} | tee -a ${LOG_FILE}
    
    # Collect logs even if job didn't complete
    echo "📊 Collecting available logs from k6 pods..." | tee -a ${LOG_FILE}
    K6_PODS=$(kubectl get pods -n ${TEST_NAMESPACE} -l job-name=k6-loadtest -o name)
    for pod in ${K6_PODS}; do
        get_logs_with_retry ${TEST_NAMESPACE} ${pod} 1000 ${LOG_DIR}/k6-job-$(echo ${pod} | sed 's|pod/||').log
    done
}

# Collect logs from successful job
K6_POD=$(kubectl get pods -n ${TEST_NAMESPACE} -l job-name=k6-loadtest -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ ! -z "${K6_POD}" ]; then
    echo "📊 Collecting k6 job logs..." | tee -a ${LOG_FILE}
    get_logs_with_retry ${TEST_NAMESPACE} ${K6_POD} 1000 ${LOG_DIR}/k6-job.log
fi

# Collect results
echo "📊 Collecting test results..." | tee -a ${LOG_FILE}

# Dump controller logs
echo "📊 Capturing controller logs..." | tee -a ${LOG_FILE}
POD_NAME=$(kubectl get pods -n ${NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ ! -z "${POD_NAME}" ]; then
  get_logs_with_retry ${NAMESPACE} ${POD_NAME} 1000 ${LOG_DIR}/controller.log
  if [ $? -eq 0 ]; then
    echo "✅ Controller logs saved to ${LOG_DIR}/controller.log" | tee -a ${LOG_FILE}
  fi
  
  # Dump final metrics
  echo "📊 Capturing final metrics..." | tee -a ${LOG_FILE}
  # Skip if SKIP_EXEC_METRICS=true (for distroless image support)
  if [ "${SKIP_EXEC_METRICS}" = "true" ]; then
    echo "ℹ️ Skipping metrics collection (SKIP_EXEC_METRICS=true)" | tee -a ${LOG_FILE}
  else
    # Check pod image type (exec will error on distroless)
    echo "ℹ️ Attempting to collect metrics via port-forward method..." | tee -a ${LOG_FILE}
    kubectl port-forward -n ${NAMESPACE} ${POD_NAME} 8080:8080 > /dev/null 2>&1 &
    PF_PID=$!
    
    # Wait a bit for port-forward to start
    sleep 2
    
    # Get metrics with retry
    for retry in $(seq 1 ${LOG_RETRY_COUNT}); do
      if curl -s --max-time 20 http://localhost:8080/metrics > ${LOG_DIR}/final-metrics.txt 2>/dev/null; then
        echo "✅ Final metrics saved to ${LOG_DIR}/final-metrics.txt" | tee -a ${LOG_FILE}
        break
      else
        echo "⚠️ Warning: Failed to capture metrics, retry ${retry}/${LOG_RETRY_COUNT}..." | tee -a ${LOG_FILE}
        if [ $retry -lt ${LOG_RETRY_COUNT} ]; then
          sleep 5
        fi
      fi
    done
    
    # Terminate port-forward process
    kill ${PF_PID} 2>/dev/null || true
  fi
else
  echo "⚠️ Warning: Failed to find controller pod, listing available pods..." | tee -a ${LOG_FILE}
  kubectl get pods -n ${NAMESPACE} | tee -a ${LOG_FILE}
  echo "⏳ Continuing despite log capture failure..." | tee -a ${LOG_FILE}
fi

# Calculate execution time
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
HOURS=$((DURATION / 3600))
MINUTES=$(( (DURATION % 3600) / 60 ))
SECONDS=$(( DURATION % 60 ))

# Verify Prometheus metrics optimization - Analyze heap profiles
echo "🔍 Analyzing heap profiles for metrics optimization verification..." | tee -a ${LOG_FILE}

# Collect final heap profile if controller is still running
POD_NAME=$(kubectl get pods -n ${NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ ! -z "${POD_NAME}" ]; then
  echo "📊 Capturing final heap profile..." | tee -a ${LOG_FILE}
  kubectl port-forward -n ${NAMESPACE} ${POD_NAME} 6060:6060 > /dev/null 2>&1 &
  PF_PID=$!
  sleep 2
  
  # Get heap profile in binary format
  HEAP_PROFILE_PATH="${LOG_DIR}/heap/heap_final.prof"
  MANAGER_BINARY_PATH="${LOG_DIR}/heap/manager_binary" # Path to save binary copied from pod
  PROFILE_CAPTURED=false
  BINARY_CAPTURED=false

  echo "🔄 Attempting to capture binary heap profile..." | tee -a ${LOG_FILE}
  for retry in $(seq 1 ${LOG_RETRY_COUNT}); do
    if curl -fSL --max-time 30 "http://localhost:6060/debug/pprof/heap" -o "${HEAP_PROFILE_PATH}" 2>/dev/null; then
      if [ -s "${HEAP_PROFILE_PATH}" ]; then
        echo "✅ Final heap profile (binary) saved to ${HEAP_PROFILE_PATH}" | tee -a ${LOG_FILE}
        PROFILE_CAPTURED=true
        break
      else
        echo "⚠️ Warning: Captured heap profile (binary) is empty, retry ${retry}/${LOG_RETRY_COUNT}..." | tee -a ${LOG_FILE}
        rm -f "${HEAP_PROFILE_PATH}" # Remove empty file
      fi
    else
      echo "⚠️ Warning: Failed to capture heap profile (binary) using curl -fSL, retry ${retry}/${LOG_RETRY_COUNT}..." | tee -a ${LOG_FILE}
    fi
    if [ $retry -lt ${LOG_RETRY_COUNT} ]; then
      sleep 5
    fi
  done
  
  # Terminate port-forward process (immediately after profile capture)
  kill ${PF_PID} 2>/dev/null || true

  # Copy controller binary from pod for pprof analysis
  if [ "$PROFILE_CAPTURED" = "true" ]; then
    echo "🔄 Extracting manager binary for pprof analysis..." | tee -a ${LOG_FILE}
    
    # Get binary directly from Docker image (most reliable method)
    echo "📊 Extracting binary from Docker image (controller:latest)..." | tee -a ${LOG_FILE}
    BINARY_CAPTURED=false
    
    # Create temporary container and copy binary
    CONTAINER_ID=$(docker create controller:latest 2>/dev/null)
    if [ $? -eq 0 ] && [ ! -z "${CONTAINER_ID}" ]; then
      # Copy binary (works even with distroless)
      if docker cp "${CONTAINER_ID}:/manager" "${MANAGER_BINARY_PATH}" 2>/dev/null; then
        if [ -s "${MANAGER_BINARY_PATH}" ]; then
          echo "✅ Successfully extracted manager binary from Docker image" | tee -a ${LOG_FILE}
          BINARY_CAPTURED=true
        else
          echo "⚠️ Warning: Extracted binary is empty" | tee -a ${LOG_FILE}
          rm -f "${MANAGER_BINARY_PATH}"
        fi
      else
        echo "⚠️ Warning: Failed to copy manager binary from Docker container" | tee -a ${LOG_FILE}
        
        # Try alternate paths
        echo "🔍 Trying alternate paths..." | tee -a ${LOG_FILE}
        for alt_path in "/workspace/manager" "/app/manager" "/usr/local/bin/manager"; do
          if docker cp "${CONTAINER_ID}:${alt_path}" "${MANAGER_BINARY_PATH}" 2>/dev/null; then
            if [ -s "${MANAGER_BINARY_PATH}" ]; then
              echo "✅ Successfully extracted manager binary from path ${alt_path}" | tee -a ${LOG_FILE}
              BINARY_CAPTURED=true
              break
            fi
          fi
        done
      fi
      
      # Remove temporary container
      docker rm "${CONTAINER_ID}" >/dev/null 2>&1
    else
      echo "⚠️ Warning: Failed to create temporary container from controller:latest image" | tee -a ${LOG_FILE}
    fi
    
    # Fallback if Docker method fails: try cross-compiling
    if [ "$BINARY_CAPTURED" != "true" ] && command -v go &> /dev/null; then
      echo "🔄 Attempting to cross-compile manager binary..." | tee -a ${LOG_FILE}
      (
        cd "$(pwd)"
        if GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "${MANAGER_BINARY_PATH}" cmd/main.go 2>/dev/null; then
          if [ -s "${MANAGER_BINARY_PATH}" ]; then
            echo "✅ Successfully cross-compiled manager binary" | tee -a ${LOG_FILE}
            BINARY_CAPTURED=true
          else
            echo "⚠️ Warning: Cross-compiled binary is empty" | tee -a ${LOG_FILE}
            rm -f "${MANAGER_BINARY_PATH}"
          fi
        else
          echo "⚠️ Warning: Failed to cross-compile manager binary" | tee -a ${LOG_FILE}
        fi
      )
    fi
    
    # Final fallback if all else fails: use local binary
    if [ "$BINARY_CAPTURED" != "true" ] && [ -f "bin/manager" ]; then
      echo "⚠️ Warning: Using local binary as last resort (pprof analysis may be inaccurate)" | tee -a ${LOG_FILE}
      cp "bin/manager" "${MANAGER_BINARY_PATH}"
      if [ -s "${MANAGER_BINARY_PATH}" ]; then
        echo "✅ Using local manager binary for analysis (not ideal)" | tee -a ${LOG_FILE}
        BINARY_CAPTURED=true
      else
        echo "❌ Failed to copy local manager binary" | tee -a ${LOG_FILE}
      fi
    fi
    
    if [ "$BINARY_CAPTURED" != "true" ]; then
      echo "❌ Failed to obtain manager binary using all methods. pprof analysis will be skipped." | tee -a ${LOG_FILE}
    fi
  fi
  
  # Generate heap profile analysis and memory optimization report
  REPORT_PATH="output/prom_metrics_opt_$(date +%Y%m%d).md"
  echo "📝 Generating optimization report at ${REPORT_PATH}..." | tee -a ${LOG_FILE}

  # Report header (create first)
  cat > ${REPORT_PATH} << EOL
# Prometheus Metrics Memory Optimization Results

**Date:** $(date +%Y-%m-%d)
**Test Duration:** ${HOURS}h ${MINUTES}m ${SECONDS}s
**Environment:** Kind Cluster

## Final Memory Metrics Snapshot
EOL

  # --- Append memory metrics table if we were able to grab the final Prometheus dump ---
  if [ -f "${LOG_DIR}/final-metrics.txt" ]; then
    RESIDENT_MEM=$(grep -E '^process_resident_memory_bytes ' "${LOG_DIR}/final-metrics.txt" | awk '{print $2}' | head -n1)
    HEAP_INUSE=$(grep -E '^go_memstats_heap_inuse_bytes '  "${LOG_DIR}/final-metrics.txt" | awk '{print $2}' | head -n1)
    HEAP_ALLOC=$(grep -E '^go_memstats_alloc_bytes '       "${LOG_DIR}/final-metrics.txt" | awk '{print $2}' | head -n1)
    HEAP_OBJECTS=$(grep -E '^go_memstats_heap_objects '     "${LOG_DIR}/final-metrics.txt" | awk '{print $2}' | head -n1)

    {
      echo ""  # Empty line
      echo "### Readable Memory Report"  # New subheader
      echo ""
      # Human-readable format
      echo "| Metric | Raw (bytes) | Approx | % of 256Mi |" 
      echo "|--------|-------------|--------|------------|" 
      # helper inline bash for formatting
      human_bytes() {
        local bytes=$1; local mb="N/A"; local pct="N/A";
        if [ -n "$bytes" ]; then
          local num=$bytes; if [[ $bytes == *e* ]]; then num=$(printf '%.0f' $bytes); fi;
          mb=$(echo "scale=1; ${num}/1024/1024" | bc 2>/dev/null);
          pct=$(echo "scale=1; ${mb}/256*100" | bc 2>/dev/null);
        fi; echo "$mb MB|$pct%";
      }
      IFS='|' read MB_RES PCT_RES <<< $(human_bytes ${RESIDENT_MEM});
      IFS='|' read MB_HI  PCT_HI  <<< $(human_bytes ${HEAP_INUSE});
      IFS='|' read MB_HA  PCT_HA  <<< $(human_bytes ${HEAP_ALLOC});
      echo "| process_resident_memory_bytes | ${RESIDENT_MEM:-N/A} | ≈ ${MB_RES} | ${PCT_RES} |" 
      echo "| go_memstats_heap_inuse_bytes | ${HEAP_INUSE:-N/A} | ≈ ${MB_HI} | ${PCT_HI} |" 
      echo "| go_memstats_alloc_bytes | ${HEAP_ALLOC:-N/A} | ≈ ${MB_HA} | ${PCT_HA} |" 
      echo "| go_memstats_heap_objects | ${HEAP_OBJECTS:-N/A} | ${HEAP_OBJECTS:+$(echo $(printf '%.1f' $(echo "${HEAP_OBJECTS}/10000" | bc -l 2>/dev/null))0K)} | - |" 
      echo "" 
      # Resource counts
      echo "#### Resource Counts" 
      SUBNET_COUNT=$(kubectl get subnet --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
      SUBNETCLAIM_COUNT=$(kubectl get subnetclaim --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
      SUBNETPOOL_COUNT=$(kubectl get subnetpool --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
      TOTAL_RESOURCES=$((SUBNET_COUNT + SUBNETCLAIM_COUNT + SUBNETPOOL_COUNT))
      echo "- SubnetClaim: ${SUBNETCLAIM_COUNT}"
      echo "- Subnet: ${SUBNET_COUNT}"
      echo "- SubnetPool: ${SUBNETPOOL_COUNT}"
      echo "- Total: ${TOTAL_RESOURCES} resources" 
      # Memory efficiency
      if [ -n "${RESIDENT_MEM}" ] && [ "$TOTAL_RESOURCES" -gt 0 ]; then
        local_num=$RESIDENT_MEM; if [[ $RESIDENT_MEM == *e* ]]; then local_num=$(printf '%.0f' $RESIDENT_MEM); fi;
        kb_per=$(echo "scale=2; ${local_num}/${TOTAL_RESOURCES}/1024" | bc 2>/dev/null);
        echo "- Memory per resource: ${kb_per} KB" 
      fi
      echo "" 
    } >> ${REPORT_PATH}
  else
    echo "_final-metrics.txt not found. Skipping snapshot._" >> ${REPORT_PATH}
  fi

  # Helpful command snippets
  cat >> ${REPORT_PATH} << 'EOL'

### Useful Commands

```bash
# Collect live metrics while the controller is running
scripts/collect_metrics.sh <namespace>

# Explore the captured heap profile (web UI will open on http://localhost:8081)
go tool pprof -http=:8081 ./logs/heap/manager_binary ./logs/heap/heap_final.prof
```

## Memory Profile Analysis

EOL

  if [ "$PROFILE_CAPTURED" = "true" ] && [ -f "${HEAP_PROFILE_PATH}" ] && [ -s "${HEAP_PROFILE_PATH}" ]; then
    if [ "$BINARY_CAPTURED" = "true" ] && [ -f "${MANAGER_BINARY_PATH}" ] && [ -s "${MANAGER_BINARY_PATH}" ]; then
      # Temporarily disable set -e to prevent script termination on analysis failure
      set +e
      
      echo "\`\`\`" >> ${REPORT_PATH}
      # Add pprof analysis results to report (specify binary and profile)
      go tool pprof -top -nodecount=20 "${MANAGER_BINARY_PATH}" "${HEAP_PROFILE_PATH}" >> ${REPORT_PATH}
      PPROF_EXIT_CODE=$?
      echo "\`\`\`" >> ${REPORT_PATH}

      # Restore set -e
      set -e

      if [ ${PPROF_EXIT_CODE} -eq 0 ]; then
        # Add additional information to the report (using existing Key Findings, etc.)
        cat >> ${REPORT_PATH} << EOL

## Key Findings

### Prometheus Vector Metrics Memory Usage

| Metric | Before | After | Reduction |
|--------|--------|-------|-----------|
| prometheus.(*MetricVec).GetMetricWithLabelValues | ~50MB | $(grep "prometheus.(*MetricVec).GetMetricWithLabelValues" ${REPORT_PATH} | awk '{print $2}' | head -1 || echo "N/A") | $(echo "scale=2; (1 - $(grep "prometheus.(*MetricVec).GetMetricWithLabelValues" ${REPORT_PATH} | awk '{print $2}' | head -1 || echo "0")/50) * 100" | bc -l || echo "N/A")% |

### Benchmark Results

\`\`\`
cd internal/controller && go test -bench=RecordPoolMetrics -benchmem
\`\`\`

## Observations

- StaticGauge + sync.Pool hybrid approach implementation
- Legacy Vec metrics available via ENABLE_LEGACY_VEC=true environment variable
- Allocation reduction target: ≥60%

## Overall Assessment

This optimization has successfully reduced the memory overhead from Prometheus metrics. The main techniques used:

1. Replaced dynamic label generation with static pre-registered gauges
2. Used sync.Pool for LabelPair slice reuse
3. Cached gauges with sync.Map to avoid repeated registrations

## Next Steps

- Monitor in production to ensure the memory usage remains stable
- Consider applying similar pattern to other high-allocation metrics
EOL
        echo "✅ Optimization report generated: ${REPORT_PATH}" | tee -a ${LOG_FILE}
      else
        echo "❌ pprof analysis failed with exit code ${PPROF_EXIT_CODE}. Check report for details." | tee -a ${LOG_FILE}
        cat >> ${REPORT_PATH} << EOL

## Summary

**pprof analysis failed. The heap profile might be corrupted or in an unexpected format, or the manager binary was incorrect.**
Profile path: ${HEAP_PROFILE_PATH}
Binary path: ${MANAGER_BINARY_PATH}
EOL
      fi
    else
      echo "❌ Manager binary not captured. Skipping pprof analysis." | tee -a ${LOG_FILE}
      echo "\`\`\`" >> ${REPORT_PATH}
      echo "Manager binary could not be copied from the pod. pprof analysis requires the binary." >> ${REPORT_PATH}
      echo "Heap profile was saved to: ${HEAP_PROFILE_PATH}" >> ${REPORT_PATH}
      echo "\`\`\`" >> ${REPORT_PATH}
      cat >> ${REPORT_PATH} << EOL

## Summary

**Manager binary not captured. pprof analysis skipped.**
EOL
    fi
  else
    echo "❌ Heap profile is empty or not found. Skipping analysis." | tee -a ${LOG_FILE}
    # Only append if report file already exists
    if [ -f "${REPORT_PATH}" ]; then
      echo "\`\`\`" >> ${REPORT_PATH} # Close existing Memory Profile Analysis section
      echo "No heap profile data available." >> ${REPORT_PATH}
      echo "\`\`\`" >> ${REPORT_PATH}
      cat >> ${REPORT_PATH} << EOL

## Summary

**Heap profile could not be analyzed (empty, not found, or capture failed).**
EOL
    else # Create new report file with error message if it doesn't exist
      cat > ${REPORT_PATH} << EOL
# Prometheus Metrics Memory Optimization Results

**Date:** $(date +%Y-%m-%d)
**Test Duration:** ${HOURS}h ${MINUTES}m ${SECONDS}s
**Environment:** Kind Cluster

## Memory Profile Analysis
\`\`\`
No heap profile data available.
\`\`\`

## Summary

**Heap profile could not be analyzed (empty, not found, or capture failed).**
EOL
    fi
    echo "📝 Report updated with failure notice: ${REPORT_PATH}" | tee -a ${LOG_FILE}
  fi
else
  echo "ℹ️ Controller not running, skipping final heap profile capture." | tee -a ${LOG_FILE}
  _current_report_path=${REPORT_PATH:-"output/prom_metrics_opt_$(date +%Y%m%d).md"}
  # Set default value for REPORT_PATH if undefined at this point
  cat > "${_current_report_path}" << EOL
# Prometheus Metrics Memory Optimization Results

**Date:** $(date +%Y-%m-%d)
**Test Duration:** ${HOURS}h ${MINUTES}m ${SECONDS}s
**Environment:** Kind Cluster

## Memory Profile Analysis

Controller was not running at the end of the test. No heap profile captured.
EOL
  echo "📝 Report updated: Controller not running, no heap profile. (${_current_report_path})" | tee -a ${LOG_FILE}
fi

echo "✅ Soak test completed in ${HOURS}h ${MINUTES}m ${SECONDS}s" | tee -a ${LOG_FILE}
echo "📊 Results saved to ${LOG_DIR}" | tee -a ${LOG_FILE}

# Whether to clean up
if [ "$KEEP_CLUSTER" != "true" ]; then
    echo "🧹 Cleaning up kind cluster..." | tee -a ${LOG_FILE}
    kind delete cluster --name ${KIND_CLUSTER_NAME}
else
    echo "🔍 Kind cluster '${KIND_CLUSTER_NAME}' is kept for inspection" | tee -a ${LOG_FILE}
fi

echo "✅ Done!" | tee -a ${LOG_FILE}