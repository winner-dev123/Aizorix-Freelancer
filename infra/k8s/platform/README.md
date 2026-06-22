# Platform layer

Cluster-wide controllers that the application workloads depend on. These are installed
**once per cluster**, before the app overlays. We bootstrap them with Argo CD using the
app-of-apps pattern (`app-of-apps.yaml`), so the only manual step is installing Argo CD
itself and applying the root Application.

## Components

| Component                 | Purpose                                                            | Install |
|---------------------------|-------------------------------------------------------------------|---------|
| AWS Load Balancer Controller | Provisions ALB/NLB from Ingress/Service objects (north-south)  | Helm    |
| ingress-nginx (optional)  | Alternative/edge ingress for non-ALB routing                      | Helm    |
| External Secrets Operator | Syncs AWS Secrets Manager -> k8s Secrets (see `cluster-secret-store.yaml`) | Helm |
| cert-manager              | TLS cert issuance (ACME/Let's Encrypt) for internal/edge TLS      | Helm    |
| Karpenter                 | Just-in-time node provisioning (SPOT + on-demand) — the autoscaler | Helm  |
| Argo CD                   | GitOps controller; owns everything via app-of-apps                | Helm    |

## Prerequisites (created by Terraform)

- IRSA roles for each controller (LB controller, external-secrets, cert-manager, Karpenter)
  follow the same pattern as `modules/iam`; create them alongside the service roles or in a
  dedicated `controllers` block. Each controller's ServiceAccount is annotated with its role.
- The OIDC provider, node groups, and VPC subnet discovery tags (`kubernetes.io/role/elb`,
  `karpenter.sh/discovery`) are already set by `modules/vpc` / `modules/eks`.

## Bootstrap order

```bash
# 1. Install Argo CD (Helm) into the argocd namespace.
helm repo add argo https://argoproj.github.io/argo-helm
helm upgrade --install argocd argo/argo-cd -n argocd --create-namespace

# 2. Apply the root app-of-apps; Argo CD then reconciles every child Application:
#    controllers (LB/external-secrets/cert-manager/karpenter) THEN the app overlays.
kubectl apply -f infra/k8s/platform/app-of-apps.yaml

# 3. Once External Secrets is healthy, apply the ClusterSecretStore.
kubectl apply -f infra/k8s/platform/cluster-secret-store.yaml
```

## Notes

- **Helm values** for each controller live in the Git repo paths referenced by the child
  Applications (`infra/k8s/platform/charts/<name>/values.yaml`). They are intentionally not
  duplicated here; the manifests in this directory are the cluster-level glue (store, issuer,
  app-of-apps) that is hand-authored rather than chart-managed.
- The AWS LB Controller is preferred at the edge (native ALB + WAF + TLS offload). Use
  `ingress-nginx` only where you need features ALB lacks; both can coexist by IngressClass.
