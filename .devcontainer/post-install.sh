#!/bin/bash
set -euo pipefail

echo "===================================="
echo "Kubebuilder DevContainer Setup"
echo "===================================="

# Verify running as root (required for installing to /usr/local/bin and /etc)
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: This script must be run as root"
  exit 1
fi

echo ""
echo "Detecting system architecture..."
# Detect architecture using uname
MACHINE=$(uname -m)
case "${MACHINE}" in
  x86_64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    echo "WARNING: Unsupported architecture ${MACHINE}, defaulting to amd64"
    ARCH="amd64"
    ;;
esac
echo "Architecture: ${ARCH}"

echo ""
echo "------------------------------------"
echo "Setting up bash completion..."
echo "------------------------------------"

BASH_COMPLETIONS_DIR="/usr/share/bash-completion/completions"

# Enable bash-completion in root's .bashrc (devcontainer runs as root)
if ! grep -q "source /usr/share/bash-completion/bash_completion" ~/.bashrc 2>/dev/null; then
  echo 'source /usr/share/bash-completion/bash_completion' >> ~/.bashrc
  echo "Added bash-completion to .bashrc"
fi

echo ""
echo "------------------------------------"
echo "Installing development tools..."
echo "------------------------------------"

# Install kind
if ! command -v kind &> /dev/null; then
  echo "Installing kind..."
  curl -Lo /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/latest/kind-linux-${ARCH}"
  chmod +x /usr/local/bin/kind
  echo "kind installed successfully"
fi

# Generate kind bash completion
if command -v kind &> /dev/null; then
  if kind completion bash > "${BASH_COMPLETIONS_DIR}/kind" 2>/dev/null; then
    echo "kind completion installed"
  else
    echo "WARNING: Failed to generate kind completion"
  fi
fi

# Install kubebuilder
if ! command -v kubebuilder &> /dev/null; then
  echo "Installing kubebuilder..."
  curl -Lo /usr/local/bin/kubebuilder "https://go.kubebuilder.io/dl/latest/linux/${ARCH}"
  chmod +x /usr/local/bin/kubebuilder
  echo "kubebuilder installed successfully"
fi

# Generate kubebuilder bash completion
if command -v kubebuilder &> /dev/null; then
  if kubebuilder completion bash > "${BASH_COMPLETIONS_DIR}/kubebuilder" 2>/dev/null; then
    echo "kubebuilder completion installed"
  else
    echo "WARNING: Failed to generate kubebuilder completion"
  fi
fi

# Install kubectl
if ! command -v kubectl &> /dev/null; then
  echo "Installing kubectl..."
  KUBECTL_VERSION=$(curl -Ls https://dl.k8s.io/release/stable.txt)
  curl -Lo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"
  chmod +x /usr/local/bin/kubectl
  echo "kubectl installed successfully"
fi

# Generate kubectl bash completion
if command -v kubectl &> /dev/null; then
  if kubectl completion bash > "${BASH_COMPLETIONS_DIR}/kubectl" 2>/dev/null; then
    echo "kubectl completion installed"
  else
    echo "WARNING: Failed to generate kubectl completion"
  fi
fi

# Generate Docker bash completion
if command -v docker &> /dev/null; then
  if docker completion bash > "${BASH_COMPLETIONS_DIR}/docker" 2>/dev/null; then
    echo "docker completion installed"
  else
    echo "WARNING: Failed to generate docker completion"
  fi
fi

echo ""
echo "------------------------------------"
echo "Building git from source..."
echo "------------------------------------"

# The repo uses extensions.relativeWorktrees (written by host git >= 2.48);
# Debian trixie ships git 2.47.3, which refuses to open such a repository.
# Not tracked by Renovate: bump by hand, updating both the version and the
# sha256 from https://mirrors.edge.kernel.org/pub/software/scm/git/sha256sums.asc
GIT_VERSION=2.53.0
GIT_SHA256=5818bd7d80b061bbbdfec8a433d609dc8818a05991f731ffc4a561e2ca18c653
if [ "$(git --version 2>/dev/null)" != "git version ${GIT_VERSION}" ]; then
  apt-get update
  apt-get install -y --no-install-recommends \
    gcc make xz-utils libcurl4-openssl-dev libssl-dev zlib1g-dev libexpat1-dev
  GIT_BUILD_DIR="$(mktemp -d)"
  curl -sSfLo "${GIT_BUILD_DIR}/git.tar.xz" "https://mirrors.edge.kernel.org/pub/software/scm/git/git-${GIT_VERSION}.tar.xz"
  echo "${GIT_SHA256}  ${GIT_BUILD_DIR}/git.tar.xz" | sha256sum -c -
  tar xJ -C "${GIT_BUILD_DIR}" -f "${GIT_BUILD_DIR}/git.tar.xz"
  make -C "${GIT_BUILD_DIR}/git-${GIT_VERSION}" -j"$(nproc)" prefix=/usr/local \
    NO_TCLTK=1 NO_GETTEXT=1 NO_PERL=1 NO_PYTHON=1 install
  rm -rf "${GIT_BUILD_DIR}"
  hash -r
fi
# /usr/local/bin must win over the feature-installed /usr/bin/git
[ "$(command -v git)" = "/usr/local/bin/git" ] || { echo "ERROR: git on PATH is $(command -v git), not /usr/local/bin/git"; exit 1; }
git --version

echo ""
echo "------------------------------------"
echo "Installing pinned tools..."
echo "------------------------------------"

# Renovate tracks these pins (.github/renovate.json). Twins elsewhere in the
# repo, to keep in step: helm-unittest in .taskfiles/chart.yml; yq in
# .github/workflows/ci-chart.yml; golangci-lint in Makefile and
# .github/actions/setup/action.yml; govulncheck and hadolint in
# .github/actions/setup/action.yml. task and helm are pinned only here.
# Installed unconditionally (this script runs once, on create).

# renovate: datasource=github-releases depName=go-task/task
TASK_VERSION=v3.54.0
curl -sSfL "https://github.com/go-task/task/releases/download/${TASK_VERSION}/task_linux_${ARCH}.tar.gz" \
  | tar xz -C /usr/local/bin task

# renovate: datasource=github-releases depName=helm/helm
HELM_VERSION=v4.3.2
curl -sSfL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz" \
  | tar xz --strip-components=1 -C /usr/local/bin "linux-${ARCH}/helm"

# renovate: datasource=github-releases depName=helm-unittest/helm-unittest
HELM_UNITTEST_VERSION=v1.2.1
helm plugin install https://github.com/helm-unittest/helm-unittest --version "${HELM_UNITTEST_VERSION}" --verify=false

# renovate: datasource=github-releases depName=mikefarah/yq
YQ_VERSION=v4.54.3
curl -sSfLo /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${ARCH}"
chmod +x /usr/local/bin/yq

# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION=2.15.0
curl -sSfL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${ARCH}.tar.gz" \
  | tar xz --strip-components=1 -C /usr/local/bin "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${ARCH}/golangci-lint"

# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION=v1.9.0
GOBIN=/usr/local/bin go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"

# renovate: datasource=github-releases depName=hadolint/hadolint
HADOLINT_VERSION=v2.15.1
case "${ARCH}" in
  amd64) HADOLINT_ASSET="hadolint-Linux-x86_64" ;;
  arm64) HADOLINT_ASSET="hadolint-Linux-arm64" ;;
esac
curl -sSfLo /usr/local/bin/hadolint "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/${HADOLINT_ASSET}"
chmod +x /usr/local/bin/hadolint

apt-get update
apt-get install -y --no-install-recommends yamllint

# The worktree is owned by the host uid
git config --global --add safe.directory '*'

echo ""
echo "------------------------------------"
echo "Configuring Docker environment..."
echo "------------------------------------"

# Wait for Docker to be ready
echo "Waiting for Docker to be ready..."
for i in {1..30}; do
  if docker info >/dev/null 2>&1; then
    echo "Docker is ready"
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "WARNING: Docker not ready after 30s"
  fi
  sleep 1
done

# Create kind network (ignore if already exists)
if ! docker network inspect kind >/dev/null 2>&1; then
  if docker network create kind >/dev/null 2>&1; then
    echo "Created kind network"
  else
    echo "WARNING: Failed to create kind network (may already exist)"
  fi
fi

echo ""
echo "------------------------------------"
echo "Verifying installations..."
echo "------------------------------------"
kind version
kubebuilder version
kubectl version --client
docker --version
go version
task --version
helm version --short
helm plugin list
yq --version
golangci-lint --version
govulncheck -version
hadolint --version
yamllint --version

echo ""
echo "===================================="
echo "DevContainer ready!"
echo "===================================="
echo "All development tools installed successfully."
echo "You can now start building Kubernetes operators."
