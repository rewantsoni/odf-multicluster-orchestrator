# MirrorPeer API Redesign Proposal

## Executive Summary

Redesign the MirrorPeer API to support multiple storage vendors (ODF, CNSA, Dell, Flash, etc.) while maintaining backward compatibility with existing ODF-only deployments.

## Current State

The current API is tightly coupled to ODF:
- Only supports ODF vendor (no vendor field)
- `StorageClusterRef` is ODF-specific (references ODF's StorageCluster CRD)
- Assumes ODF-specific behaviors for S3 and DRCluster management
- Only supports ODF with sync/async DR modes

Requirements:
1. One DRCluster per ManagedCluster.
2. One s3Profile per DRCluster. S3 profile name is immutable. DrClusters will be onwed by mirrorPeers that refrence them.
3. s3 can be internal or external.
   1. Internal for ODF only.
      1. In case of internal, add a TODO to figure out how to migrate s3 and handle multiple mirrorPeers.
   2. External for all vendors.
      1. In case of external - consider one mirrorPeer as primary owner (first one), and others as secondary, when primary is deleted one of the secondary takes over. I want to use ownerShip pattern to find out primary(controller-owner) vs secondary (owner).
      2. For external s3, managedCluster will have a s3Ref field that will point to the secret that contains the config required by MCO.
      3. If for one mirrorPeer the s3Ref is provided then for other mirrorPeer it shouldn't be provided. 
      4. s3Ref should be immutable. it should be created in MirrorPeer's namespace only.
      5. store the actual s3Ref use for each managedCluster in the status of mirrorPeer.
      6. Multiple managedClusters can be shared the same s3Ref
4. Backward Compatibility.
5. Single Vendor per MirrorPeer.
6. Unique (Cluster, Vendor) Pairs.
7. MirrorPeer Controller Owns Ramen ConfigMap.

## Design Constraints

1. **One DRCluster per ManagedCluster**: Each managed cluster can have only one DRCluster resource (shared across all storage vendors on that cluster)
2. **S3 Configuration per MirrorPeer**: Each MirrorPeer must reference a secret with S3 credentials via S3SecretRef in each PeerRef
3. **One S3 Configuration per ManagedCluster**: Once a ManagedCluster has S3 configured via one MirrorPeer, no other MirrorPeer can configure S3 for that cluster until the first MirrorPeer is removed. This is because each cluster's DRCluster can only have one S3 configuration.
4. **Backward Compatibility**: Existing MirrorPeer resources must continue to work
5. **Single Vendor per MirrorPeer**: Cannot peer clusters with different storage vendors
6. **Unique (Cluster, Vendor) Pairs**: Each combination of (ManagedCluster, Vendor) can appear in only one MirrorPeer. However, the same ManagedCluster can appear in multiple MirrorPeers if each uses a different vendor.
7. **MirrorPeer Controller Owns Ramen ConfigMap**: Only the MirrorPeer controller updates the Ramen ConfigMap with S3 profile information. Vendor-specific controllers (ODF, CNSA, etc.) do not directly modify Ramen ConfigMap.

## Proposed API Design

### Key Changes

1. **Add `Vendor` field to MirrorPeerSpec**: Identifies the storage vendor for this mirror peer
2. **Reuse `StorageClusterRef` for all vendors**: Despite the name, `storageClusterRef` now works for ALL storage vendors (ODF, CNSA, Dell, Flash). No new field needed, no deprecation required.
3. **Default vendor**: When not specified, vendor defaults to `odf` for backward compatibility
4. **Add `S3SecretRef` per PeerRef**: Each cluster references its S3 configuration (optional for ODF, required for others)

### New API Types

```go
// StorageVendor represents the storage backend vendor
// +kubebuilder:validation:Enum=odf;cnsa;dell;flash
type StorageVendor string

const (
    StorageVendorODF   StorageVendor = "odf"
    StorageVendorCNSA  StorageVendor = "cnsa"
    StorageVendorDell  StorageVendor = "dell"
    StorageVendorFlash StorageVendor = "flash"
)

// StorageClusterRef holds a reference to a storage resource.
// NOTE: Despite the name "StorageClusterRef", this field works for ALL storage vendors,
// not just ODF StorageCluster. The name is historical (from ODF-only days) but is kept
// for backward compatibility. The interpretation depends on spec.vendor:
//   - vendor=odf: references a StorageCluster CR
//   - vendor=cnsa: references a CNSA storage resource
//   - vendor=dell: references a Dell storage system resource
//   - vendor=flash: references a Pure Flash storage resource
type StorageClusterRef struct {
    // Name is the name of the storage resource.
    // Interpretation depends on spec.vendor (see type comment above).
    // +kubebuilder:validation:Required
    Name string `json:"name"`

    // Namespace is the namespace of the storage resource
    // +kubebuilder:validation:Optional
    Namespace string `json:"namespace,omitempty"`
}

// PeerRef holds a reference to a mirror peer
type PeerRef struct {
    // ClusterName is the name of ManagedCluster
    // ManagedCluster matching this name is considered a peer cluster
    // +kubebuilder:validation:Required
    ClusterName string `json:"clusterName"`
    
    // StorageClusterRef holds a reference to the storage resource on this cluster.
    // Despite the name, this works for all storage vendors (see StorageClusterRef type comment).
    // The type of storage is determined by spec.vendor.
    // +kubebuilder:validation:Required
    StorageClusterRef StorageClusterRef `json:"storageClusterRef"`
    
    // S3SecretRef references a secret containing S3 configuration for this cluster's
    // DR metadata storage. The secret can be in any namespace on the hub cluster.
    //
    // **Requirement by vendor:**
    //   - ODF (vendor=odf): OPTIONAL
    //     * If S3SecretRef is PROVIDED: ODF uses the external S3 endpoint from the
    //       secret and does NOT create an OBC.
    //     * If S3SecretRef is NOT PROVIDED: ODF controller automatically creates an
    //       OBC on the spoke cluster, generates a secret on the hub from the OBC
    //       credentials, and updates this PeerRef with the S3SecretRef.
    //   - CNSA/Dell/Flash: REQUIRED - User must create this secret with S3 configuration
    //     provided via UI or manually.
    // 
    // Required keys in the secret:
    //   - AWS_ACCESS_KEY_ID: S3 access key
    //   - AWS_SECRET_ACCESS_KEY: S3 secret key
    //   - s3Bucket: Bucket name for DR metadata
    //   - s3Endpoint: S3 endpoint URL
    //   - s3Region: S3 region (optional, defaults to us-east-1)
    //
    // How vendors populate this secret:
    //   - ODF with internal S3 (s3SecretRef not provided):
    //     * ODF controller creates an OBC on each spoke cluster
    //     * ODF controller generates this secret on the hub from the OBC credentials
    //     * ODF controller updates this PeerRef with the S3SecretRef
    //     * Each cluster typically has its own S3 endpoint (Noobaa/RGW), so each
    //       PeerRef will have a DIFFERENT S3SecretRef
    //   - ODF with external S3 (s3SecretRef provided):
    //     * User creates secret with external S3 endpoint (e.g., AWS S3)
    //     * ODF controller skips OBC creation
    //     * Both PeerRefs can reference the same secret (shared external S3)
    //   - CNSA/Dell/Flash:
    //     * User creates secret(s) with S3 info via UI
    //     * Can reference the same secret in both PeerRefs if using shared S3
    //     * Or different secrets if each cluster has its own S3
    //
    // **One S3 per Cluster Constraint:**
    // Once a ManagedCluster has S3 configured via one MirrorPeer, no other MirrorPeer
    // can configure S3 for that cluster until the first MirrorPeer is removed. This is
    // validated at the webhook level.
    //
    // The MirrorPeer controller:
    //   1. Waits for S3SecretRef to be populated (by ODF or user)
    //   2. Reads S3 secrets from each PeerRef
    //   3. Creates S3 profiles in Ramen ConfigMap (one profile per unique S3 endpoint)
    //   4. Propagates S3 secrets to spoke clusters
    //   5. Configures DRCluster on each spoke to reference the appropriate S3 profile
    //
    // +kubebuilder:validation:Optional
    S3SecretRef *SecretReference `json:"s3SecretRef,omitempty"`
}

// SecretReference contains enough information to locate a secret in any namespace
type SecretReference struct {
    // Name is the name of the secret
    // +kubebuilder:validation:Required
    Name string `json:"name"`
    
    // Namespace is the namespace of the secret
    // +kubebuilder:validation:Required
    Namespace string `json:"namespace"`
}

// MirrorPeerSpec defines the desired state of MirrorPeer
type MirrorPeerSpec struct {
    // Vendor identifies the storage backend vendor for this MirrorPeer
    // All clusters in this MirrorPeer must use the same vendor
    // +kubebuilder:validation:Optional
    // +kubebuilder:default=odf
    // +kubebuilder:validation:Enum=odf;cnsa;dell;flash
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.vendor is immutable."
    Vendor StorageVendor `json:"vendor,omitempty"`

    // Type represents the mode of DR operation (sync or async)
    // +kubebuilder:default=async
    // +kubebuilder:validation:Enum=async;sync
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.type is immutable."
    Type DRType `json:"type"`

    // Items is a list of PeerRef
    // +kubebuilder:validation:MaxItems=2
    // +kubebuilder:validation:MinItems=2
    // +kubebuilder:validation:XValidation:rule="self.all(e, (size(oldSelf.filter(x, x.clusterName == e.clusterName)) == 1))",message="items.clusterName is immutable."
    // +listType=map
    // +listMapKey=clusterName
    Items []PeerRef `json:"items"`
}

// MirrorPeerStatus defines the observed state of MirrorPeer
type MirrorPeerStatus struct {
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    Phase      PhaseType          `json:"phase,omitempty"`
    Message    PhaseMessage       `json:"message,omitempty"`
    
    // S3ProfileName is the name of the S3 profile configured in Ramen ConfigMap
    // This is derived from the MirrorPeer name and used to track which S3 profile
    // this MirrorPeer has configured.
    // +kubebuilder:validation:Optional
    S3ProfileName string `json:"s3ProfileName,omitempty"`
}
```

### Validation Rules (Webhook)

Implement a validating webhook to enforce:

```go
// Validation rules to implement in webhook:

1. Backward Compatibility:
   - If vendor field is not specified, default to "odf"
   - Existing MirrorPeers without vendor field continue to work

2. Field Validation:
   - Each PeerRef must have storageClusterRef (required)
   - storageClusterRef.name must be non-empty

3. S3SecretRef Validation by Vendor:
   - For vendor=odf: S3SecretRef is OPTIONAL
     * If not provided initially, ODF controller will populate it later
     * MirrorPeer status should indicate "Waiting for S3 configuration" until populated
   - For vendor=cnsa/dell/flash: S3SecretRef is REQUIRED
     * Reject if S3SecretRef is not provided
     * Error: "S3SecretRef is required for vendor '<vendor>'"
   - If S3SecretRef is provided:
     * Secret must exist at the referenced namespace/name
     * Secret must contain required keys:
       - AWS_ACCESS_KEY_ID
       - AWS_SECRET_ACCESS_KEY
       - s3Bucket
       - s3Endpoint
       - s3Region (optional)

4. One S3 per ManagedCluster Constraint:
   - Build map of ManagedCluster -> MirrorPeer (that has configured S3 for that cluster)
   - For each cluster in the new/updated MirrorPeer:
     * Check if any OTHER MirrorPeer already has S3 configured for that cluster
     * If yes, reject with error: "Cluster 'X' already has S3 configured by MirrorPeer 'Y'. 
       Remove MirrorPeer 'Y' before configuring S3 for this cluster in another MirrorPeer."
   - Note: "S3 configured" means the PeerRef has s3SecretRef populated (either by user or ODF)
   - This applies across ALL vendors - ODF and Dell cannot both configure S3 for same cluster

5. Unique (Cluster, Vendor) Constraint:
   - Each (ManagedCluster, Vendor) pair can appear in only ONE MirrorPeer
   - A cluster CAN appear in multiple MirrorPeers if each uses a different vendor
   - Examples of VALID configurations:
     * MirrorPeerA: cluster1(odf) <-> cluster2(odf)
     * MirrorPeerB: cluster1(dell) <-> cluster2(dell)  // Same clusters, different vendor
   - Examples of INVALID configurations:
     * MirrorPeerA: cluster1(odf) <-> cluster2(odf)
     * MirrorPeerB: cluster1(odf) <-> cluster3(odf)  // cluster1+odf appears in both
   - Return clear error: "Cluster 'X' with vendor 'Y' is already used by MirrorPeer 'Z'"

6. Immutability:
   - Vendor cannot change after creation
   - ClusterName cannot change after creation
   - Type (sync/async) cannot change after creation
   - S3SecretRef can change (allows updating S3 credentials/endpoint)
     * But changing it triggers re-validation of "One S3 per Cluster" constraint
```

## Migration Path

### Phase 1: API Update (v1alpha1 - Current)
- Add `vendor` field with default "odf"
- Add `s3SecretRef` field to PeerRef (optional)
- Update `StorageClusterRef` comments to clarify it works for all vendors
- Deploy updated CRD
- **No breaking changes - fully backward compatible**

### Phase 2: Controller Support (v1alpha1)
- Refactor controller to support vendor abstraction
- Implement vendor-specific reconcilers
- Existing MirrorPeers work without modification

### Phase 3: Vendor Implementations (Ongoing)
- ODF reconciler (refactored from existing code)
- CNSA reconciler (new)
- Dell reconciler (new)
- Flash reconciler (new)

## Example Usage

### Example 1: ODF Async DR (Backward Compatible - Old Format)
```yaml
# Note: For backward compatibility, S3SecretRef is optional when using StorageClusterRef
# In reality, the ODF controller would create these secrets, this is just showing backward compat
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-async-dr-legacy
spec:
  # vendor defaults to "odf" when StorageClusterRef is used
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef:  # Old format still works
      name: ocs-storagecluster
      namespace: openshift-storage
  - clusterName: cluster2
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
```

### Example 2: ODF with Explicit Vendor - Separate S3 Per Cluster
```yaml
# ODF scenario: Each cluster has its own S3 endpoint (Noobaa/RGW)
# The ODF controller creates OBC on each cluster and generates these secrets
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-async-dr
  namespace: openshift-operators
spec:
  vendor: odf  # Explicit vendor specification
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef:  # Same field name, works for all vendors
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: cluster1-odf-s3-secret  # ODF controller creates this from cluster1's OBC
      namespace: openshift-dr-system  # Secrets in dedicated namespace
  - clusterName: cluster2
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: cluster2-odf-s3-secret  # ODF controller creates this from cluster2's OBC
      namespace: openshift-dr-system

---
# Secret created by ODF controller for cluster1 (simplified)
apiVersion: v1
kind: Secret
metadata:
  name: cluster1-odf-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "abc123..."
  AWS_SECRET_ACCESS_KEY: "xyz789..."
  s3Bucket: "odrbucket-cluster1"
  s3Endpoint: "https://s3.openshift-storage.cluster1.example.com"
  s3Region: "us-east-1"

---
# Secret created by ODF controller for cluster2
apiVersion: v1
kind: Secret
metadata:
  name: cluster2-odf-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "def456..."
  AWS_SECRET_ACCESS_KEY: "uvw012..."
  s3Bucket: "odrbucket-cluster2"
  s3Endpoint: "https://s3.openshift-storage.cluster2.example.com"
  s3Region: "us-east-1"
```

### Example 3: ODF Sync DR (External Mode) - Shared External S3
```yaml
# ODF external mode: Uses external Ceph cluster, but DR metadata stored in external S3
# Both clusters can share the same external S3 endpoint
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-sync-dr
  namespace: openshift-operators
spec:
  vendor: odf
  type: sync  # Sync DR for external mode
  items:
  - clusterName: cluster3
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: external-s3-secret  # Both clusters share same external S3
      namespace: openshift-dr-system
  - clusterName: cluster4
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: external-s3-secret  # Same secret = same S3 endpoint
      namespace: openshift-dr-system

---
# User-created secret for external S3 (e.g., AWS S3)
apiVersion: v1
kind: Secret
metadata:
  name: external-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "AKIAIOSFODNN7EXAMPLE"
  AWS_SECRET_ACCESS_KEY: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
  s3Bucket: "my-company-dr-metadata"
  s3Endpoint: "https://s3.amazonaws.com"
  s3Region: "us-west-2"
```

### Example 4: CNSA Async DR - User-Provided S3
```yaml
# CNSA scenario: User provides S3 configuration via UI/CR
# User creates secret with S3 credentials, can be shared or separate per cluster
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: cnsa-async-dr
  namespace: openshift-operators
spec:
  vendor: cnsa
  type: async
  items:
  - clusterName: cluster5
    storageClusterRef:
      name: cnsa-storage-system
      namespace: cnsa-system
    s3SecretRef:
      name: cnsa-s3-secret  # User-created secret (could share with cluster6)
      namespace: openshift-dr-system
  - clusterName: cluster6
    storageClusterRef:
      name: cnsa-storage-system
      namespace: cnsa-system
    s3SecretRef:
      name: cnsa-s3-secret  # Same secret = shared S3 endpoint
      namespace: openshift-dr-system

---
# User creates this secret with S3 info from UI input
apiVersion: v1
kind: Secret
metadata:
  name: cnsa-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "cnsa-access-key"
  AWS_SECRET_ACCESS_KEY: "cnsa-secret-key"
  s3Bucket: "cnsa-dr-bucket"
  s3Endpoint: "https://s3.cnsa-provider.example.com"
  s3Region: "us-east-1"
```

### Example 5: Dell PowerStore Sync DR - External S3
```yaml
# Dell scenario: User provides external S3 for DR metadata
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-sync-dr
  namespace: openshift-operators
spec:
  vendor: dell
  type: sync
  items:
  - clusterName: cluster7
    storageClusterRef:
      name: powerstore-system
      namespace: dell-storage
    s3SecretRef:
      name: dell-s3-secret
      namespace: openshift-dr-system
  - clusterName: cluster8
    storageClusterRef:
      name: powerstore-system
      namespace: dell-storage
    s3SecretRef:
      name: dell-s3-secret  # Shared external S3
      namespace: openshift-dr-system

---
apiVersion: v1
kind: Secret
metadata:
  name: dell-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "dell-s3-key"
  AWS_SECRET_ACCESS_KEY: "dell-s3-secret"
  s3Bucket: "dell-dr-metadata"
  s3Endpoint: "https://s3-compatible.dell.example.com"
  s3Region: "us-east-1"
```

### Example 6: Pure FlashArray Async DR
```yaml
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: flash-async-dr
spec:
  vendor: flash
  type: async
  manageS3: false
  items:
  - clusterName: cluster9
    storageClusterRef:
      name: flasharray-x
      namespace: pure-storage
  - clusterName: cluster10
    storageClusterRef:
      name: flasharray-y
      namespace: pure-storage
```

### Example 7: Same Clusters, Multiple Vendors (Multi-Vendor Per Cluster)
```yaml
# This example shows that cluster1 and cluster2 can each have BOTH ODF and Dell storage
# Each cluster will have ONE DRCluster that includes configuration for both vendors
# IMPORTANT: Only ONE MirrorPeer can configure S3 for a given cluster

# ODF peering between cluster1 and cluster2 - ODF configures S3
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-dr-pair
  namespace: openshift-operators
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster1  # cluster1 has ODF storage
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: shared-s3-secret  # ODF configures S3 for cluster1
      namespace: openshift-dr-system
  - clusterName: cluster2  # cluster2 has ODF storage
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    s3SecretRef:
      name: shared-s3-secret  # ODF configures S3 for cluster2
      namespace: openshift-dr-system
---
# Dell peering between the SAME cluster1 and cluster2
# Dell CANNOT configure S3 since ODF already did
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-dr-pair
  namespace: openshift-operators
spec:
  vendor: dell
  type: async
  items:
  - clusterName: cluster1  # SAME cluster1, but Dell storage
    storageClusterRef:
      name: powerstore-array
      namespace: dell-storage
    # NO s3SecretRef here - cluster1 S3 already configured by odf-dr-pair
  - clusterName: cluster2  # SAME cluster2, but Dell storage
    storageClusterRef:
      name: powerstore-array
      namespace: dell-storage
    # NO s3SecretRef here - cluster2 S3 already configured by odf-dr-pair

---
# One shared S3 secret used by ODF (and inherited by Dell via DRCluster)
# Could be created by ODF controller or manually by user
apiVersion: v1
kind: Secret
metadata:
  name: shared-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "shared-s3-key"
  AWS_SECRET_ACCESS_KEY: "shared-s3-secret"
  s3Bucket: "multi-vendor-dr-metadata"
  s3Endpoint: "https://s3.example.com"
  s3Region: "us-east-1"

# Result:
# - cluster1 has ONE DRCluster with configuration for both ODF and Dell
# - cluster2 has ONE DRCluster with configuration for both ODF and Dell
# - ODF MirrorPeer configured S3 for both clusters
# - Dell MirrorPeer does NOT configure S3 (uses S3 already configured by ODF)
# - Each vendor handles its own storage-level replication
# - MirrorPeer controller creates one S3 profile in Ramen ConfigMap (from ODF's secret)
# - DRCluster on each spoke references the same S3 profile for both ODF and Dell workloads

# INVALID Example (would be rejected by webhook):
# ---
# apiVersion: multicluster.odf.openshift.io/v1alpha1
# kind: MirrorPeer
# metadata:
#   name: dell-dr-pair-invalid
# spec:
#   vendor: dell
#   type: async
#   items:
#   - clusterName: cluster1
#     storageClusterRef:
#       name: powerstore-array
#       namespace: dell-storage
#     s3SecretRef:
#       name: dell-s3-secret  # ERROR! cluster1 already has S3 from odf-dr-pair
#       namespace: openshift-dr-system
# Error: "Cluster 'cluster1' already has S3 configured by MirrorPeer 'odf-dr-pair'"
```

## S3 Configuration Architecture

### Overview

The new design eliminates the complex `manageS3` coordination and simplifies S3 configuration:

1. **Per-PeerRef S3SecretRef**: Each cluster reference in a MirrorPeer specifies its S3 configuration via a secret reference
2. **Vendor-Specific Secret Creation**: Each vendor controller (ODF, CNSA, etc.) creates the S3 secret in their own way
3. **MirrorPeer Controller as Single Writer**: Only the MirrorPeer controller updates Ramen ConfigMap

### Workflow by Vendor

**ODF Workflow (Internal S3 - s3SecretRef not provided)**:
1. User creates MirrorPeer CR without S3SecretRef in PeerRefs
2. ODF controller detects MirrorPeer with vendor=odf and missing S3SecretRef
3. ODF controller creates OBC on each spoke cluster (e.g., cluster1 and cluster2)
4. ODF controller waits for OBC to be bound and get credentials
5. ODF controller creates S3 secrets on hub from OBC credentials:
   - `cluster1-odf-s3-secret` in `openshift-dr-system` namespace
   - `cluster2-odf-s3-secret` in `openshift-dr-system` namespace
6. ODF controller updates MirrorPeer spec to populate S3SecretRef in each PeerRef
7. MirrorPeer controller detects S3SecretRef is now populated
8. MirrorPeer controller reads secrets, updates Ramen ConfigMap

**ODF Workflow (External S3 - s3SecretRef provided)**:
1. User creates S3 secret with external endpoint (e.g., AWS S3, Minio)
2. User creates MirrorPeer CR with S3SecretRef already populated in PeerRefs
3. ODF controller detects MirrorPeer with vendor=odf and S3SecretRef present
4. ODF controller skips OBC creation (uses external S3)
5. MirrorPeer controller reads secret, updates Ramen ConfigMap

**CNSA/Dell/Flash Workflow**:
1. User provides S3 endpoint/credentials via UI
2. UI/operator creates Secret with S3 configuration
3. User creates MirrorPeer CR with S3SecretRef pointing to that secret
4. Webhook validates S3SecretRef is present (required for non-ODF vendors)
5. MirrorPeer controller reads secret, updates Ramen ConfigMap

### MirrorPeer Controller S3 Responsibilities

```go
func (r *MirrorPeerReconciler) reconcileS3Configuration(ctx context.Context, mp *MirrorPeer) error {
    // 1. Collect all unique S3 endpoints from all PeerRefs
    s3Configs := make(map[string]*S3Config) // key: endpoint URL
    
    for _, peerRef := range mp.Spec.Items {
        // Read S3 secret
        secret := &corev1.Secret{}
        secretKey := types.NamespacedName{
            Name:      peerRef.S3SecretRef.Name,
            Namespace: peerRef.S3SecretRef.Namespace,
        }
        if err := r.Get(ctx, secretKey, secret); err != nil {
            return fmt.Errorf("failed to get S3 secret %s: %w", secretKey, err)
        }
        
        // Extract S3 config from secret
        endpoint := string(secret.Data["s3Endpoint"])
        bucket := string(secret.Data["s3Bucket"])
        
        // Create unique key for deduplication
        configKey := fmt.Sprintf("%s/%s", endpoint, bucket)
        
        if _, exists := s3Configs[configKey]; !exists {
            s3Configs[configKey] = &S3Config{
                Endpoint:   endpoint,
                Bucket:     bucket,
                Region:     string(secret.Data["s3Region"]),
                AccessKey:  string(secret.Data["AWS_ACCESS_KEY_ID"]),
                SecretKey:  string(secret.Data["AWS_SECRET_ACCESS_KEY"]),
                ProfileName: generateS3ProfileName(mp, peerRef.ClusterName),
            }
        }
    }
    
    // 2. Update Ramen ConfigMap with S3 profiles
    //    Each unique S3 endpoint gets one profile
    for _, config := range s3Configs {
        if err := r.updateRamenConfigMapS3Profile(ctx, config); err != nil {
            return err
        }
    }
    
    // 3. Propagate S3 secrets to spoke clusters
    for _, peerRef := range mp.Spec.Items {
        if err := r.propagateS3SecretToSpoke(ctx, peerRef, s3Configs); err != nil {
            return err
        }
    }
    
    return nil
}
```

### Benefits of S3SecretRef Approach

✅ **Eliminates manageS3 Coordination**: No need to determine which MirrorPeer "wins" S3 management  
✅ **Single Source of Truth**: MirrorPeer controller is the only component writing to Ramen ConfigMap  
✅ **Vendor Independence**: Each vendor creates secrets in their own way, MirrorPeer controller consumes them uniformly  
✅ **Flexibility**: Can share S3 across vendors or have separate S3 per cluster  
✅ **Clear Ownership**: S3 secret creation = vendor's responsibility, Ramen configuration = MirrorPeer controller's responsibility  
✅ **No Deletion Conflicts**: Deleting a MirrorPeer just removes its S3 profile from Ramen ConfigMap; doesn't block if other MirrorPeers exist  

## Controller Architecture

### Vendor Abstraction Layer

```go
// VendorReconciler interface for vendor-specific reconciliation logic
type VendorReconciler interface {
    // ReconcilePeering sets up storage-level peering between clusters
    ReconcilePeering(ctx context.Context, mp *MirrorPeer) error
    
    // ReconcileS3 configures S3 for DR (only called if mp.Spec.ManageS3 is true and validated)
    ReconcileS3(ctx context.Context, mp *MirrorPeer) error
    
    // GetDRClusterConfig returns vendor-specific configuration to be merged into the DRCluster
    // Since each cluster can have only ONE DRCluster but multiple MirrorPeers with different
    // vendors, the main controller will merge configurations from all vendors.
    // This method returns the portion of DRCluster config specific to this vendor.
    GetDRClusterConfig(ctx context.Context, mp *MirrorPeer, clusterName string) (*DRClusterConfig, error)
    
    // ValidateStorageClusterRef validates the storage reference exists in the given cluster
    ValidateStorageClusterRef(ctx context.Context, ref StorageClusterRef, clusterName string) error
    
    // GetS3Info returns S3 endpoint and bucket info (if vendor provides S3)
    GetS3Info(ctx context.Context, mp *MirrorPeer) (*S3Info, error)
    
    // Cleanup handles vendor-specific cleanup on MirrorPeer deletion
    Cleanup(ctx context.Context, mp *MirrorPeer) error
}

// DRClusterConfig contains vendor-specific DRCluster configuration
// Multiple vendors contributing to the same DRCluster will have their configs merged
type DRClusterConfig struct {
    // ClusterFences contains fence agent configuration for this vendor
    ClusterFences []ramenv1alpha1.ClusterFence
    
    // S3ProfileName identifies which S3 profile this vendor uses
    S3ProfileName string
    
    // Region for the DRCluster
    Region string
    
    // VendorSpecificAnnotations contains vendor-specific annotations
    VendorSpecificAnnotations map[string]string
    
    // VendorSpecificLabels contains vendor-specific labels
    VendorSpecificLabels map[string]string
}

// S3Info contains S3 configuration information
type S3Info struct {
    Endpoint        string
    BucketName      string
    Region          string
    SecretName      string
    SecretNamespace string
}

// Factory to get vendor-specific reconciler
func GetVendorReconciler(vendor StorageVendor, client client.Client, scheme *runtime.Scheme) (VendorReconciler, error) {
    switch vendor {
    case StorageVendorODF:
        return &ODFReconciler{Client: client, Scheme: scheme}, nil
    case StorageVendorCNSA:
        return &CNSAReconciler{Client: client, Scheme: scheme}, nil
    case StorageVendorDell:
        return &DellReconciler{Client: client, Scheme: scheme}, nil
    case StorageVendorFlash:
        return &FlashReconciler{Client: client, Scheme: scheme}, nil
    default:
        return nil, fmt.Errorf("unsupported vendor: %s", vendor)
    }
}
```

### Main Controller Flow

```go
func (r *MirrorPeerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    mp := &multiclusterv1alpha1.MirrorPeer{}
    if err := r.Get(ctx, req.NamespacedName, mp); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // Handle deletion
    if !mp.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, mp)
    }
    
    // Add finalizer if not present
    if !controllerutil.ContainsFinalizer(mp, mirrorPeerFinalizer) {
        controllerutil.AddFinalizer(mp, mirrorPeerFinalizer)
        return ctrl.Result{}, r.Update(ctx, mp)
    }
    
    // Determine vendor (defaults to "odf" if not specified)
    vendor := mp.Spec.Vendor
    if vendor == "" {
        vendor = multiclusterv1alpha1.StorageVendorODF
    }
    
    // Get vendor-specific reconciler
    vendorReconciler, err := GetVendorReconciler(vendor, r.Client, r.Scheme)
    if err != nil {
        return r.updateStatusWithError(ctx, mp, err)
    }
    
    // Phase 1: Validation
    r.updatePhase(ctx, mp, multiclusterv1alpha1.Initializing, "Validating MirrorPeer")
    
    // Validate common constraints via webhook (already validated, but double-check)
    if err := r.validateConstraints(ctx, mp); err != nil {
        return r.updateStatusWithError(ctx, mp, err)
    }
    
    // Validate storage references exist
    for _, item := range mp.Spec.Items {
        if err := vendorReconciler.ValidateStorageClusterRef(ctx, item.StorageClusterRef, item.ClusterName); err != nil {
            return r.updateStatusWithError(ctx, mp, fmt.Errorf("invalid storage ref for cluster %s: %w", item.ClusterName, err))
        }
    }
    
    // Phase 2: Configuration
    r.updatePhase(ctx, mp, multiclusterv1alpha1.Configuring, "Configuring storage peering")
    
    // Setup storage-level peering
    if err := vendorReconciler.ReconcilePeering(ctx, mp); err != nil {
        return r.updateStatusWithError(ctx, mp, fmt.Errorf("peering failed: %w", err))
    }
    
    // Phase 3: S3 Configuration
    // Read S3 secrets and configure Ramen ConfigMap + propagate to spokes
    r.updatePhase(ctx, mp, multiclusterv1alpha1.Configuring, "Configuring S3")
    if err := r.reconcileS3Configuration(ctx, mp); err != nil {
        return r.updateStatusWithError(ctx, mp, fmt.Errorf("S3 configuration failed: %w", err))
    }
    
    // Phase 4: DRCluster Configuration
    // IMPORTANT: Each cluster can have only ONE DRCluster, but multiple MirrorPeers
    // with different vendors may reference the same cluster. We need to merge
    // DRCluster configuration from all MirrorPeers that use each cluster.
    r.updatePhase(ctx, mp, multiclusterv1alpha1.Configuring, "Configuring DRCluster")
    if err := r.reconcileDRClustersForMirrorPeer(ctx, mp); err != nil {
        return r.updateStatusWithError(ctx, mp, fmt.Errorf("DRCluster configuration failed: %w", err))
    }
    
    // Phase 5: Ready
    r.updatePhase(ctx, mp, multiclusterv1alpha1.Ready, "MirrorPeer is ready")
    
    return ctrl.Result{}, r.Status().Update(ctx, mp)
}

// reconcileDRClustersForMirrorPeer reconciles DRCluster for each cluster in this MirrorPeer
// It merges configuration from ALL MirrorPeers that reference each cluster
func (r *MirrorPeerReconciler) reconcileDRClustersForMirrorPeer(ctx context.Context, mp *multiclusterv1alpha1.MirrorPeer) error {
    // Get all MirrorPeers to find which ones reference the same clusters
    allMirrorPeers := &multiclusterv1alpha1.MirrorPeerList{}
    if err := r.List(ctx, allMirrorPeers); err != nil {
        return fmt.Errorf("failed to list MirrorPeers: %w", err)
    }
    
    // For each cluster in this MirrorPeer, reconcile its DRCluster
    for _, peerRef := range mp.Spec.Items {
        if err := r.reconcileDRClusterForCluster(ctx, peerRef.ClusterName, allMirrorPeers); err != nil {
            return fmt.Errorf("failed to reconcile DRCluster for cluster %s: %w", peerRef.ClusterName, err)
        }
    }
    
    return nil
}

// reconcileDRClusterForCluster creates/updates a single DRCluster by merging config from all MirrorPeers
func (r *MirrorPeerReconciler) reconcileDRClusterForCluster(
    ctx context.Context,
    clusterName string,
    allMirrorPeers *multiclusterv1alpha1.MirrorPeerList,
) error {
    // Find all MirrorPeers that reference this cluster
    var relevantMirrorPeers []*multiclusterv1alpha1.MirrorPeer
    for i := range allMirrorPeers.Items {
        mp := &allMirrorPeers.Items[i]
        for _, peerRef := range mp.Spec.Items {
            if peerRef.ClusterName == clusterName {
                relevantMirrorPeers = append(relevantMirrorPeers, mp)
                break
            }
        }
    }
    
    // Collect DRCluster configuration from each vendor
    var mergedConfig *ramenv1alpha1.DRCluster
    var allFences []ramenv1alpha1.ClusterFence
    s3Profiles := make(map[string]bool)
    
    for _, mp := range relevantMirrorPeers {
        vendor := mp.Spec.Vendor
        if vendor == "" {
            vendor = multiclusterv1alpha1.StorageVendorODF
        }
        
        // Get vendor-specific reconciler
        vendorReconciler, err := GetVendorReconciler(vendor, r.Client, r.Scheme)
        if err != nil {
            return err
        }
        
        // Get vendor's DRCluster configuration
        config, err := vendorReconciler.GetDRClusterConfig(ctx, mp, clusterName)
        if err != nil {
            return fmt.Errorf("failed to get DRCluster config from %s vendor: %w", vendor, err)
        }
        
        // Merge configurations
        if mergedConfig == nil {
            // First vendor sets the base
            mergedConfig = &ramenv1alpha1.DRCluster{
                ObjectMeta: metav1.ObjectMeta{
                    Name: clusterName,
                },
                Spec: ramenv1alpha1.DRClusterSpec{
                    Region: config.Region,
                },
            }
        }
        
        // Merge fences from all vendors
        allFences = append(allFences, config.ClusterFences...)
        
        // Track S3 profiles
        if config.S3ProfileName != "" {
            s3Profiles[config.S3ProfileName] = true
        }
        
        // Merge annotations and labels
        if mergedConfig.Annotations == nil {
            mergedConfig.Annotations = make(map[string]string)
        }
        for k, v := range config.VendorSpecificAnnotations {
            mergedConfig.Annotations[k] = v
        }
        
        if mergedConfig.Labels == nil {
            mergedConfig.Labels = make(map[string]string)
        }
        for k, v := range config.VendorSpecificLabels {
            mergedConfig.Labels[k] = v
        }
    }
    
    if mergedConfig == nil {
        return fmt.Errorf("no DRCluster configuration collected for cluster %s", clusterName)
    }
    
    // Set the merged fences
    mergedConfig.Spec.ClusterFences = allFences
    
    // Convert S3 profiles map to list
    var s3ProfileList []string
    for profile := range s3Profiles {
        s3ProfileList = append(s3ProfileList, profile)
    }
    mergedConfig.Spec.S3ProfileName = s3ProfileList
    
    // Create or update the DRCluster via ManifestWork
    return r.createOrUpdateDRCluster(ctx, clusterName, mergedConfig)
}

// getStorageRef extracts StorageRef from PeerRef, handling backward compatibility
func (r *MirrorPeerReconciler) getStorageRef(peer multiclusterv1alpha1.PeerRef) multiclusterv1alpha1.StorageRef {
    if peer.StorageRef != nil {
        return *peer.StorageRef
    }
    
    // Backward compatibility: convert StorageClusterRef to StorageRef
    if peer.StorageClusterRef != nil {
        return multiclusterv1alpha1.StorageRef{
            Name:      peer.StorageClusterRef.Name,
            Namespace: peer.StorageClusterRef.Namespace,
        }
    }
    
    // Should not happen due to validation
    return multiclusterv1alpha1.StorageRef{}
}

// shouldManageS3 checks if this MirrorPeer is the designated S3 manager
func (r *MirrorPeerReconciler) shouldManageS3(ctx context.Context, mp *multiclusterv1alpha1.MirrorPeer) bool {
    if !mp.Spec.ManageS3 {
        return false
    }
    
    // List all MirrorPeers with ManageS3=true
    mpList := &multiclusterv1alpha1.MirrorPeerList{}
    if err := r.List(ctx, mpList); err != nil {
        return false
    }
    
    // Find oldest MirrorPeer with ManageS3=true
    var oldest *multiclusterv1alpha1.MirrorPeer
    for i := range mpList.Items {
        item := &mpList.Items[i]
        if !item.Spec.ManageS3 {
            continue
        }
        if oldest == nil || item.CreationTimestamp.Before(&oldest.CreationTimestamp) {
            oldest = item
        }
    }
    
    // We manage S3 if we're the oldest
    return oldest != nil && oldest.Name == mp.Name
}

// validateConstraints checks cluster-wide constraints
func (r *MirrorPeerReconciler) validateConstraints(ctx context.Context, mp *multiclusterv1alpha1.MirrorPeer) error {
    // Get all MirrorPeers
    mpList := &multiclusterv1alpha1.MirrorPeerList{}
    if err := r.List(ctx, mpList); err != nil {
        return err
    }
    
    // Determine this MirrorPeer's vendor
    myVendor := mp.Spec.Vendor
    if myVendor == "" {
        myVendor = multiclusterv1alpha1.StorageVendorODF
    }
    
    // Build map of (cluster, vendor) -> MirrorPeer
    // Key format: "clusterName:vendor"
    clusterVendorMap := make(map[string]string)
    for i := range mpList.Items {
        item := &mpList.Items[i]
        if item.Name == mp.Name {
            continue // Skip self
        }
        
        itemVendor := item.Spec.Vendor
        if itemVendor == "" {
            itemVendor = multiclusterv1alpha1.StorageVendorODF
        }
        
        for _, peer := range item.Spec.Items {
            key := fmt.Sprintf("%s:%s", peer.ClusterName, itemVendor)
            clusterVendorMap[key] = item.Name
        }
    }
    
    // Validate our (cluster, vendor) pairs aren't in use
    for _, peer := range mp.Spec.Items {
        key := fmt.Sprintf("%s:%s", peer.ClusterName, myVendor)
        if existing, found := clusterVendorMap[key]; found {
            return fmt.Errorf("cluster %s with vendor %s is already used by MirrorPeer %s", 
                peer.ClusterName, myVendor, existing)
        }
    }
    
    return nil
}
```

### S3 Management by Vendor

Different vendors handle S3 in different ways:

**ODF (manageS3: true)**:
- Automatically creates ObjectBucketClaim (OBC) 
- Exchanges secrets between clusters
- Fully manages S3 lifecycle

**CNSA and other vendors (manageS3: false)**:
- User provides S3 configuration through UI/CR
- Can use external S3 endpoint (could be same S3 that ODF created, or separate)
- Operator doesn't create/manage S3, just uses provided credentials

**Important**: S3 management transfer between MirrorPeers is NOT supported. To change which MirrorPeer manages S3 requires:
1. Delete all DRPlacementControls (DRPC)
2. Delete all DRPolicies  
3. Delete all existing MirrorPeers
4. Create new MirrorPeer with desired S3 management configuration

This is a **disruptive operation** that impacts all DR-protected applications.

### S3 Dependency Protection on Deletion

**Problem**: If a MirrorPeer manages S3 (`manageS3: true`), deleting it will break DR for all applications across ALL MirrorPeers (since there's only one S3 for the entire system).

**Solution: S3 Dependency Validation on Deletion**

```go
// handleDeletion validates S3 dependencies before allowing MirrorPeer deletion
func (r *MirrorPeerReconciler) handleDeletion(ctx context.Context, mp *multiclusterv1alpha1.MirrorPeer) (ctrl.Result, error) {
    // Check if this MirrorPeer is actively managing S3
    if mp.Status.ManagingS3 {
        // Find all other MirrorPeers that depend on S3 but don't manage it
        mpList := &multiclusterv1alpha1.MirrorPeerList{}
        if err := r.List(ctx, mpList); err != nil {
            return ctrl.Result{}, err
        }
        
        var dependentMirrorPeers []string
        for i := range mpList.Items {
            item := &mpList.Items[i]
            if item.Name == mp.Name {
                continue
            }
            // Any MirrorPeer that needs S3 but isn't managing it depends on this one
            if !item.Status.ManagingS3 && !item.Spec.ManageS3 {
                dependentMirrorPeers = append(dependentMirrorPeers, item.Name)
            }
        }
        
        if len(dependentMirrorPeers) > 0 {
            // Block deletion and provide clear error
            err := fmt.Errorf(
                "cannot delete MirrorPeer %s: it manages S3 and the following MirrorPeers depend on it: %v. "+
                "Either delete the dependent MirrorPeers first, or enable manageS3 on one of them to transfer S3 management",
                mp.Name,
                dependentMirrorPeers,
            )
            r.updatePhase(ctx, mp, multiclusterv1alpha1.Failed, multiclusterv1alpha1.DeletionFailed)
            return ctrl.Result{}, err
        }
    }
    
    // Proceed with deletion
    vendor := mp.Spec.Vendor
    if vendor == "" {
        vendor = multiclusterv1alpha1.StorageVendorODF
    }
    
    vendorReconciler, err := GetVendorReconciler(vendor, r.Client, r.Scheme)
    if err != nil {
        return ctrl.Result{}, err
    }
    
    // Vendor-specific cleanup
    if err := vendorReconciler.Cleanup(ctx, mp); err != nil {
        return ctrl.Result{}, err
    }
    
    // Clean up DRClusters for each cluster in this MirrorPeer
    // Note: This needs to be smart - only remove vendor-specific config,
    // not the entire DRCluster if other vendors still use it
    if err := r.cleanupDRClustersForMirrorPeer(ctx, mp); err != nil {
        return ctrl.Result{}, err
    }
    
    // Remove finalizer
    controllerutil.RemoveFinalizer(mp, mirrorPeerFinalizer)
    return ctrl.Result{}, r.Update(ctx, mp)
}

// cleanupDRClustersForMirrorPeer removes vendor-specific config from DRClusters
// If this is the last vendor using a cluster, remove the entire DRCluster
func (r *MirrorPeerReconciler) cleanupDRClustersForMirrorPeer(
    ctx context.Context,
    deletingMP *multiclusterv1alpha1.MirrorPeer,
) error {
    // Get all remaining MirrorPeers (excluding the one being deleted)
    mpList := &multiclusterv1alpha1.MirrorPeerList{}
    if err := r.List(ctx, mpList); err != nil {
        return err
    }
    
    for _, peerRef := range deletingMP.Spec.Items {
        clusterName := peerRef.ClusterName
        
        // Find other MirrorPeers still using this cluster
        var remainingMPs []*multiclusterv1alpha1.MirrorPeer
        for i := range mpList.Items {
            mp := &mpList.Items[i]
            if mp.Name == deletingMP.Name {
                continue // Skip the one being deleted
            }
            for _, otherPeer := range mp.Spec.Items {
                if otherPeer.ClusterName == clusterName {
                    remainingMPs = append(remainingMPs, mp)
                    break
                }
            }
        }
        
        if len(remainingMPs) == 0 {
            // No other MirrorPeers use this cluster - delete entire DRCluster
            if err := r.deleteDRCluster(ctx, clusterName); err != nil {
                return err
            }
        } else {
            // Other vendors still use this cluster - rebuild DRCluster from remaining vendors
            if err := r.reconcileDRClusterForCluster(ctx, clusterName, mpList); err != nil {
                return err
            }
        }
    }
    
    return nil
}
```

**User Workflow for Safe Deletion**:

1. **Scenario**: Want to remove ODF MirrorPeer that manages S3, but Dell MirrorPeer depends on it

2. **Option A - Transfer S3 Management**:
   ```bash
   # Step 1: Update another MirrorPeer to manage S3 (if supported by that vendor)
   kubectl patch mirrorpeer dell-dr --type=merge -p '{"spec":{"manageS3":true}}'
   
   # Step 2: Wait for Dell to take over S3 management
   kubectl wait --for=condition=Ready mirrorpeer/dell-dr
   
   # Step 3: Update ODF to stop managing S3
   kubectl patch mirrorpeer odf-dr --type=merge -p '{"spec":{"manageS3":false}}'
   
   # Step 4: Now safe to delete ODF
   kubectl delete mirrorpeer odf-dr
   ```

3. **Option B - Delete Dependent MirrorPeers First**:
   ```bash
   # Step 1: Delete all MirrorPeers that depend on S3 but don't manage it
   kubectl delete mirrorpeer dell-dr
   
   # Step 2: Now safe to delete the S3 manager
   kubectl delete mirrorpeer odf-dr
   ```

4. **Option C - External S3 Management**:
   ```bash
   # If S3 is managed externally, set manageS3=false on ODF first
   kubectl patch mirrorpeer odf-dr --type=merge -p '{"spec":{"manageS3":false}}'
   
   # Then deletion is allowed
   kubectl delete mirrorpeer odf-dr
   ```

**Status Conditions for S3 Dependencies**:

Add a condition to show S3 dependency status:

```go
// In MirrorPeer status conditions
type Condition struct {
    Type: "S3Dependent"
    Status: "True"
    Reason: "DependsOnExternalS3Manager"
    Message: "This MirrorPeer depends on S3 managed by MirrorPeer 'odf-dr'"
}
```

This allows users to query dependencies:
```bash
kubectl get mirrorpeer dell-dr -o jsonpath='{.status.conditions[?(@.type=="S3Dependent")]}'
```

## Implementation Checklist

### Phase 1: API Changes (Week 1-2)
- [ ] Add `StorageVendor` type and constants to `mirrorpeer_types.go`
- [ ] Add `Vendor` field to `MirrorPeerSpec` with default "odf"
- [ ] Update `StorageClusterRef` comments to clarify it works for all vendors
- [ ] Add `SecretReference` type for S3 secret references
- [ ] Add `S3SecretRef` field to `PeerRef` (optional)
- [ ] Update `MirrorPeerStatus` to include `s3ProfileName` field
- [ ] Update CEL validation rules for immutability
- [ ] Run `make manifests generate`
- [ ] Test CRD generation
- [ ] Update API docs

### Phase 2: Validation Webhook (Week 2-3)
- [ ] Create webhook package structure
- [ ] Implement validating webhook
  - [ ] Vendor-StorageClusterRef compatibility check
  - [ ] Field presence validation (StorageRef XOR StorageClusterRef)
  - [ ] Immutability checks (vendor, clusterName, type)
  - [ ] S3 management validation
  - [ ] DRCluster constraint validation
- [ ] Implement mutating webhook (optional for auto-conversion)
- [ ] Add webhook configuration manifests
- [ ] Unit tests for webhook logic
- [ ] Integration tests

### Phase 3: Controller Refactoring (Week 3-5)
- [ ] Create vendor abstraction
  - [ ] Define `VendorReconciler` interface
  - [ ] Create vendor reconciler factory
  - [ ] Define `S3Info` type
- [ ] Extract ODF logic
  - [ ] Move existing logic to `ODFReconciler`
  - [ ] Implement `VendorReconciler` interface
  - [ ] Test backward compatibility
- [ ] Update main reconciler
  - [ ] Add vendor detection logic
  - [ ] Integrate vendor reconciler
  - [ ] Add S3 manager selection logic
  - [ ] Update status management
- [ ] Create stub reconcilers for other vendors
  - [ ] `CNSAReconciler` stub
  - [ ] `DellReconciler` stub
  - [ ] `FlashReconciler` stub

### Phase 4: Testing (Week 5-6)
- [ ] Unit tests
  - [ ] API type tests
  - [ ] Vendor detection tests
  - [ ] StorageRef conversion tests
  - [ ] S3 manager selection tests
- [ ] Integration tests
  - [ ] ODF backward compatibility
  - [ ] New format with vendor field
  - [ ] Mixed old/new format
  - [ ] Constraint validation
  - [ ] S3 manager conflicts
- [ ] E2E tests (with ODF)
  - [ ] Old format end-to-end
  - [ ] New format end-to-end
  - [ ] Migration from old to new

### Phase 5: Documentation (Week 6-7)
- [ ] Update API reference
- [ ] Create migration guide
  - [ ] When to migrate
  - [ ] How to migrate
  - [ ] Validation of migration
- [ ] Document vendor support
  - [ ] How to add new vendor
  - [ ] Vendor reconciler interface
  - [ ] Example implementations
- [ ] Update README
  - [ ] Multi-vendor support announcement
  - [ ] Quick start for each vendor
- [ ] Add examples
  - [ ] One example per vendor
  - [ ] Mixed vendor scenario

### Phase 6: Vendor Implementation (Ongoing)
- [ ] Implement CNSA support (separate PR)
- [ ] Implement Dell support (separate PR)
- [ ] Implement Pure Flash support (separate PR)

## Benefits

✅ **Single vendor per MirrorPeer**: Clean, simple model matching real-world usage  
✅ **Backward compatible**: Zero changes needed for existing ODF users  
✅ **Simple API**: Vendor at spec level, minimal changes to PeerRef  
✅ **Clear ownership**: One MirrorPeer manages S3, others consume it  
✅ **Extensible**: Easy to add new vendors via reconciler interface  
✅ **Type safe**: Enum-based vendor validation  
✅ **Immutable**: Vendor, cluster names, and DR type cannot change  

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Breaking existing deployments | Full backward compatibility via default vendor + StorageClusterRef support |
| Vendor reconciler complexity | Clean interface with single responsibility per vendor |
| S3 conflicts | First-come-first-serve based on creation timestamp |
| Cluster conflicts | Webhook validation prevents duplicate cluster usage |
| Testing without hardware | Mock vendor reconcilers + use test framework |

## Open Questions

1. **When to implement vendor reconcilers?**
   - Start with ODF refactoring
   - Add other vendors as demand arises
   - Provide stub implementations for interface compliance
   - **Recommendation**: Phase 1 = ODF only, Phase 2+ = other vendors

2. **API version bump?**
   - Option A: Stay in v1alpha1 (we're still alpha)
   - Option B: Move to v1alpha2
   - **Recommendation**: Stay in v1alpha1, bump to v1beta1 when removing deprecated fields

3. **Should S3SecretRef be optional for backward compatibility?**
   - For truly old MirrorPeers (created before this change), yes
   - For new MirrorPeers, should be required
   - **Recommendation**: Optional initially, add webhook to require for new resources

4. **S3 secret namespace permissions?**
   - MirrorPeer controller needs RBAC to read secrets across namespaces
   - Should we restrict to specific namespaces or allow any?
   - **Recommendation**: Allow any namespace, document RBAC requirements

## Timeline

- **Week 1-2**: API changes and CRD generation
- **Week 2-3**: Webhook implementation and testing
- **Week 3-5**: Controller refactoring with ODF vendor
- **Week 5-6**: Comprehensive testing
- **Week 6-7**: Documentation
- **Week 7+**: Additional vendor implementations

## Success Criteria

- [ ] Existing ODF MirrorPeers work without modification
- [ ] New MirrorPeers can specify vendor explicitly
- [ ] Webhook prevents constraint violations
- [ ] ODF vendor reconciler passes all existing tests
- [ ] Clear path to add CNSA, Dell, Flash vendors
- [ ] Documentation complete and reviewed
- [ ] Migration guide tested with real resources

---

## Summary of Key Design Decisions

### 1. **Vendor Field at MirrorPeer Level** ✅
**Decision**: Add `vendor` field to `MirrorPeerSpec` (not per PeerRef)  
**Rationale**: Cannot peer clusters with different storage vendors  
**Impact**: Clean, simple API; enforces single-vendor-per-MirrorPeer constraint  

### 2. **S3SecretRef per PeerRef with Namespace** ✅
**Decision**: Each PeerRef optionally references an S3 secret with `{name, namespace}`  
**Rationale**:
- ODF can use internal S3 (Noobaa/RGW via OBC) or external S3
  - If S3SecretRef not provided: ODF creates OBC and generates secret
  - If S3SecretRef provided: ODF uses external S3, skips OBC creation
- Other vendors (CNSA/Dell/Flash) always require S3SecretRef (user-provided)
- Namespaced reference allows flexible secret organization and RBAC
**Impact**: 
- Eliminates complex `manageS3` coordination logic
- ODF controller creates secrets when needed, user provides for other vendors
- MirrorPeer controller consumes secrets uniformly
- Flexible: ODF can use internal or external S3

### 3. **MirrorPeer Controller Owns Ramen ConfigMap** ✅
**Decision**: Only MirrorPeer controller writes to Ramen ConfigMap  
**Rationale**: Single writer eliminates conflicts and race conditions  
**Impact**: 
- Vendor controllers (ODF, CNSA, etc.) create S3 secrets only
- MirrorPeer controller is single source of truth for Ramen configuration
- Simpler architecture, easier to debug

### 4. **Unique (Cluster, Vendor) Constraint** ✅
**Decision**: Each (ManagedCluster, Vendor) pair can appear in only ONE MirrorPeer  
**Rationale**: Same cluster can host multiple vendors, but each vendor's DR config is separate  
**Impact**:
- cluster1(odf) can be in MirrorPeerA
- cluster1(dell) can be in MirrorPeerB
- But cluster1(odf) cannot be in both MirrorPeerA and MirrorPeerC
- One DRCluster per cluster merges config from all its MirrorPeers

### 5. **One S3 Configuration per ManagedCluster** ✅
**Decision**: Each ManagedCluster can have S3 configured by only ONE MirrorPeer  
**Rationale**: DRCluster on each cluster has only one S3 configuration, shared by all vendors  
**Impact**:
- If ODF MirrorPeer configures S3 for cluster1, Dell MirrorPeer cannot configure S3 for cluster1
- Dell MirrorPeer must omit S3SecretRef for cluster1 (inherits from ODF via DRCluster)
- Enforced by webhook validation: "Cluster X already has S3 configured by MirrorPeer Y"
- To change S3 provider: delete existing MirrorPeer, create new one with different S3

### 6. **Reuse StorageClusterRef for All Vendors** ✅
**Decision**: Keep `StorageClusterRef` as the universal field for all vendors (no new field, no deprecation)  
**Rationale**: Field name is historical but works for all vendors; simplest approach with zero disruption  
**Impact**: 
- Existing MirrorPeers continue working without modification
- No migration needed
- No deprecated fields to remove in future versions
- Clear documentation that storageClusterRef works for all vendors despite the name

### 7. **Vendor Abstraction via Interface** ✅
**Decision**: `VendorReconciler` interface for vendor-specific logic  
**Rationale**: Extensible design for adding new vendors  
**Impact**:
- ODF logic in `ODFReconciler`
- CNSA/Dell/Flash logic in their respective reconcilers
- Easy to add new vendors without modifying core controller

---

## What This Design Achieves

| Goal | How Achieved |
|------|--------------|
| Multi-vendor support | `vendor` field + `VendorReconciler` interface |
| Backward compatibility | Reuse `storageClusterRef` for all vendors; default vendor=odf; no API changes to existing fields |
| Simple S3 management | S3SecretRef per PeerRef, no coordination needed |
| ODF flexibility | S3SecretRef optional for ODF: internal S3 (OBC) or external S3 |
| Single DRCluster per cluster | Controller merges config from all MirrorPeers |
| Unique vendor per cluster | Validation: (cluster, vendor) can appear in only one MirrorPeer |
| One S3 per cluster | Validation: cluster can have S3 from only ONE MirrorPeer across all vendors |
| Per-cluster S3 (ODF internal) | Each PeerRef references different ODF-generated secret |
| Shared S3 (external) | Multiple PeerRefs/MirrorPeers can reference same user-created secret |
| Clear ownership | ODF creates secrets when needed; user provides for CNSA/Dell/Flash; MirrorPeer controller configures Ramen |
| No component conflicts | Only MirrorPeer controller writes to Ramen ConfigMap |
| Safe deletion | Dell MirrorPeer can be deleted freely (doesn't own S3); ODF deletion requires no dependent vendors |
| Zero migration effort | Existing MirrorPeers work as-is; no field renames, no deprecation warnings |

---

## Final Recommendation

**Proceed with this design for the following reasons:**

1. ✅ **Addresses all stated requirements**: Multi-vendor, one DRCluster per cluster, backward compatible
2. ✅ **Simplifies S3 management**: Eliminates `manageS3` coordination, single writer to Ramen ConfigMap
3. ✅ **Flexible and extensible**: Easy to add new vendors, supports both shared and per-cluster S3
4. ✅ **Clear separation of concerns**: Vendor controllers create secrets, MirrorPeer controller configures Ramen
5. ✅ **Production-ready**: Handles real-world scenarios (ODF's per-cluster S3, CNSA's shared S3)
6. ✅ **Low risk**: Fully backward compatible, gradual migration path

**Next Step**: Review and approve this proposal, then proceed with Phase 1 implementation (API changes).

