#!/bin/bash
set -e

echo "Installing Aquanaut CRDs..."

# CRDディレクトリのパス
CRD_DIR="internal/crds/manifests"

# CRDマニフェストを適用
for crd_file in ${CRD_DIR}/*.yaml; do
  echo "Applying CRD from ${crd_file}..."
  kubectl apply -f "${crd_file}"
done

# CRDが確立されるまで待機
echo "Waiting for CRDs to be established..."
for crd_file in ${CRD_DIR}/*.yaml; do
  # ファイル名からCRD名を正しい形式で抽出
  # plexaubnet.io_subnetclaims.yaml -> subnetclaims.plexaubnet.io
  filename=$(basename "${crd_file}" .yaml)
  resource=$(echo $filename | cut -d'_' -f2)
  group=$(echo $filename | cut -d'_' -f1)
  crd_name="${resource}.${group}"
  
  echo "Waiting for CRD ${crd_name} to be established..."
  
  # CRDが確立されるまで待機
  kubectl wait --for=condition=established --timeout=60s crd/${crd_name}
  
  if [ $? -eq 0 ]; then
    echo "CRD ${crd_name} is established."
  else
    echo "Failed to establish CRD ${crd_name}."
    exit 1
  fi
done

echo "All CRDs are installed and established successfully."