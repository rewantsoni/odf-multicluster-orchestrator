# Final API Design Summary

## Overview

Clean separation between S3 management and DR pairing:
- **S3Profile CR**: Manages all S3 configuration
- **MirrorPeer CR**: Manages DR pairing only (NO S3 fields)

---

## S3Profile API

```go
type S3ProfileSpec struct {
    // Option 1: Internal S3 (ODF) - Auto-creates OBC
    InternalS3 *InternalS3Spec `json:"internalS3,omitempty"`
    
    // Option 2: External S3 (all vendors) - Uses existing secret
    ExternalS3 *ExternalS3Spec `json:"externalS3,omitempty"`
    
    // Clusters that will use this S3 profile
    ManagedClusters []string `json:"managedClusters"`
}

type InternalS3Spec struct {
    ManagedCluster   string `json:"managedCluster"`   // Where to create OBC
    StorageClassName string `json:"storageClassName"` // e.g., "openshift-storage.noobaa.io"
    Namespace        string `json:"namespace"`        // e.g., "openshift-storage"
    OBCName          string `json:"obcName,omitempty"` // Optional
}

type ExternalS3Spec struct {
    SecretRef SecretReference `json:"secretRef"` // Reference to existing secret
}
```

---

## MirrorPeer API (No Changes to Current Struct)

```go
type PeerRef struct {
    ClusterName       string            `json:"clusterName"`
    StorageClusterRef StorageClusterRef `json:"storageClusterRef"`
    // NO S3 fields - S3Profile is auto-discovered by cluster name
}

type MirrorPeerSpec struct {
    Vendor StorageVendor `json:"vendor,omitempty"`
    Type   DRType        `json:"type"`
    Items  []PeerRef     `json:"items"`
    // manageS3 field exists in current code, will be deprecated later
}
```

---

## Example 1: ODF Internal S3

```yaml
# Step 1: Create S3Profile (OBC created on cluster1)
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: odf-internal-s3
spec:
  internalS3:
    managedCluster: cluster1        # ← OBC created here
    storageClassName: openshift-storage.noobaa.io
    namespace: openshift-storage
  managedClusters:
  - cluster1  # Uses local OBC
  - cluster2  # Uses cluster1's S3

---
# Step 2: Create MirrorPeer (S3Profile auto-discovered)
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-dr
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
```

**Result**: 
- OBC created on cluster1 via addon mechanism
- S3 endpoint: `https://s3.cluster1.example.com`
- S3 secret copied to Ramen operator namespace on hub
- DRCluster created **on hub** for cluster1: `s3ProfileName: odf-internal-s3`
- DRCluster created **on hub** for cluster2: `s3ProfileName: odf-internal-s3`
- Both DRClusters (on hub) reference cluster1's S3

---

## Example 2: External S3 (Shared)

```yaml
# Step 1: Create S3 Secret
apiVersion: v1
kind: Secret
metadata:
  name: aws-s3-secret
  namespace: openshift-dr-system
stringData:
  AWS_ACCESS_KEY_ID: "AKIA..."
  AWS_SECRET_ACCESS_KEY: "..."
  s3Bucket: "my-dr-bucket"
  s3Endpoint: "https://s3.amazonaws.com"
  s3Region: "us-west-2"

---
# Step 2: Create S3Profile
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: shared-aws-s3
spec:
  externalS3:
    secretRef:
      name: aws-s3-secret
      namespace: openshift-dr-system
  managedClusters:
  - cluster1
  - cluster2

---
# Step 3: Create MirrorPeer (same as before)
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-dr
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
```

**Result**: 
- S3 secret copied to Ramen operator namespace on hub
- DRCluster created **on hub** for cluster1: `s3ProfileName: shared-aws-s3`
- DRCluster created **on hub** for cluster2: `s3ProfileName: shared-aws-s3`
- Both DRClusters (on hub) reference AWS S3

---

## Example 3: Multi-Vendor Sharing S3

```yaml
# One S3Profile
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: shared-s3
spec:
  externalS3:
    secretRef: {name: aws-s3, namespace: openshift-dr-system}
  managedClusters: [cluster1, cluster2]

---
# ODF MirrorPeer
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-dr
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}

---
# Dell MirrorPeer (same clusters, different vendor)
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-dr
spec:
  vendor: dell
  items:
  - clusterName: cluster1
    storageClusterRef: {name: powerstore}
  - clusterName: cluster2
    storageClusterRef: {name: powerstore}
```

**Result**: 
- Both ODF and Dell auto-discover S3Profile "shared-s3"
- Both vendors share the same S3 endpoint
- DRCluster created **on hub** for cluster1 and cluster2
- Both DRClusters have `s3ProfileName: shared-s3`
- MirrorPeer controllers add themselves as owners to the DRClusters

---

## How It Works

### S3Profile Controller

1. **InternalS3 Flow**:
   - Creates OBC on `internalS3.managedCluster` via addon
   - Addon transfers generated secret from spoke → hub
   - Copies secret to Ramen operator namespace on hub
   - Updates Ramen ConfigMap on hub
   - Creates DRCluster **on hub** for each cluster in `managedClusters`
   - Sets `DRCluster.spec.s3ProfileName = <S3Profile CR name>`

2. **ExternalS3 Flow**:
   - Reads existing secret from hub
   - Copies secret to Ramen operator namespace on hub
   - Updates Ramen ConfigMap on hub
   - Creates DRCluster **on hub** for each cluster in `managedClusters`
   - Sets `DRCluster.spec.s3ProfileName = <S3Profile CR name>`

**IMPORTANT**: 
- DRCluster is a **hub-only API** (not deployed to spokes)
- S3 secrets are in Ramen operator namespace **on hub** (not on spokes)

### MirrorPeer Controller

1. For each cluster in `items`:
   - Lookup: Which S3Profile has this cluster in `managedClusters`?
   - Validate: S3Profile exists and is Ready
   - Auto-discover: No S3 fields needed in MirrorPeer

2. Setup vendor-specific peering

3. Add MirrorPeer as owner to DRCluster (already created by S3Profile)

---

## Key Design Decisions

✅ **S3Profile CR name = s3ProfileName** (no separate field)
✅ **MirrorPeer has NO S3 fields** (auto-discovered)
✅ **One cluster → One S3Profile** (validated by webhook)
✅ **OBC created on ONE cluster** (specified in `internalS3.managedCluster`)
✅ **No DRCluster labels** (S3Profile discovered by cluster lookup)
✅ **Clean separation** (S3Profile = S3 concerns, MirrorPeer = DR pairing)

---

## Migration Path

**Current**: MirrorPeer with `manageS3` field
**Phase 1**: Add S3Profile CR, support both patterns
**Phase 2**: Deprecate `manageS3` field
**Phase 3**: Remove `manageS3` field (v1beta1)

---

## Benefits

✅ S3 managed independently from DR pairing
✅ One S3Profile serves multiple vendors
✅ Flexible topology (OBC on one cluster, used by many)
✅ Simpler MirrorPeer API (no S3 fields)
✅ Easier Day 2 operations (credential rotation)
✅ Auto-discovery reduces configuration burden
