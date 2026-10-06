# MirrorPeer API Redesign - Final Design

## Requirements

1. **One DRCluster per ManagedCluster**: Each managed cluster has exactly one DRCluster resource
2. **One S3 Profile per DRCluster**: S3 profile name is immutable. DRClusters are owned by MirrorPeers that reference them
3. **S3 Types**:
   - **Internal S3 (ODF only)**: 
     - TODO: Figure out how to migrate S3 and handle multiple MirrorPeers referencing same clusters.
   - **External S3 (all vendors)**:
     - Use ownership pattern: Primary (controller-owner) vs Secondary (owner)
     - When primary deleted, secondary is promoted
     - ManagedCluster has s3Ref field pointing to secret with MCO config
     - If one MirrorPeer provides s3Ref, others cannot
     - s3Ref is immutable
     - s3Ref must be created in MirrorPeer's namespace only
     - Store actual s3Ref used per managedCluster in status
     - Multiple managedClusters can share same s3Ref
   - If ODF is created as first backend with internal s3.
     - Other backends use ODF's  s3 for DR. If user wants to disable ODF DR, they would need to remove all DRConfig and then add create it again.
   - If ODF is created as a second backend.
     - It should share the s3Ref with other mirrorPeers. 
   - Day 2 mirrorPeer creation needs us to identify if a managedCluster is already configured with a s3 or not. How can we do this easily/faster from UI?
4. **Backward Compatibility**: Existing MirrorPeers continue working
5. **Single Vendor per MirrorPeer**: Cannot mix vendors in one MirrorPeer
6. **Unique (Cluster, Vendor) Pairs**: Each (ManagedCluster, Vendor) appears in only one MirrorPeer
7. **MirrorPeer Controller Owns Ramen ConfigMap**: Only MirrorPeer controller modifies Ramen ConfigMap

---

## API Changes

### New Types

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
```

### Updated: PeerRef

```go
// PeerRef holds a reference to a mirror peer
type PeerRef struct {
    // ClusterName is the name of ManagedCluster
    // +kubebuilder:validation:Required
    ClusterName string `json:"clusterName"`
    
    // StorageClusterRef holds a reference to the storage resource on this cluster.
    // Despite the name, this works for all storage vendors:
    //   - vendor=odf: StorageCluster CR
    //   - vendor=cnsa: CNSA storage resource
    //   - vendor=dell: Dell storage resource  
    //   - vendor=flash: Pure Flash storage resource
    // +kubebuilder:validation:Required
    StorageClusterRef StorageClusterRef `json:"storageClusterRef"`
    
    // S3SecretRef references a secret containing S3 configuration for this cluster.
    // Secret can be in ANY namespace on the hub cluster.
    //
    // Behavior by vendor:
    //   - ODF: OPTIONAL
    //     * If not provided: ODF creates internal S3 (OBC/Noobaa) for this cluster
    //       Secret created in managedCluster namespace (e.g., openshift-storage)
    //     * If provided: ODF uses external S3, skips OBC creation
    //   - CNSA/Dell/Flash: REQUIRED
    //
    // Ownership pattern (when multiple PeerRefs/MirrorPeers reference same secret):
    //   - First to reference becomes PRIMARY (controller OwnerReference)
    //   - Others become SECONDARY (non-controller OwnerReference)
    //   - Primary deletion → oldest secondary promoted
    //
    // Constraints:
    //   - IMMUTABLE after set
    //   - Multiple PeerRefs can reference same secret (shared S3)
    //   - If cluster already has S3 configured, new MirrorPeer MUST use same secret
    //   - Second MirrorPeer must specify EXACT same name and namespace
    //
    // Secret format:
    //   - AWS_ACCESS_KEY_ID: S3 access key
    //   - AWS_SECRET_ACCESS_KEY: S3 secret key
    //   - s3Bucket: bucket name
    //   - s3Endpoint: endpoint URL
    //   - s3Region: region (optional)
    //
    // +kubebuilder:validation:Optional
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="s3SecretRef is immutable"
    S3SecretRef *SecretReference `json:"s3SecretRef,omitempty"`
}

// SecretReference contains namespace + name to reference a secret
type SecretReference struct {
    // Name is the name of the secret
    // +kubebuilder:validation:Required
    Name string `json:"name"`
    
    // Namespace is the namespace of the secret on the hub cluster
    // +kubebuilder:validation:Required
    Namespace string `json:"namespace"`
}
```

### Updated: MirrorPeerSpec

```go
// MirrorPeerSpec defines the desired state of MirrorPeer
type MirrorPeerSpec struct {
    // Vendor identifies the storage backend vendor
    // +kubebuilder:validation:Optional
    // +kubebuilder:default=odf
    // +kubebuilder:validation:Enum=odf;cnsa;dell;flash
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.vendor is immutable"
    Vendor StorageVendor `json:"vendor,omitempty"`

    // Type represents the DR mode (sync or async)
    // +kubebuilder:default=async
    // +kubebuilder:validation:Enum=async;sync
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.type is immutable"
    Type DRType `json:"type"`

    // Items is list of peer clusters
    // +kubebuilder:validation:MaxItems=2
    // +kubebuilder:validation:MinItems=2
    // +kubebuilder:validation:XValidation:rule="self.all(e, (size(oldSelf.filter(x, x.clusterName == e.clusterName)) == 1))",message="items.clusterName is immutable"
    // +listType=map
    // +listMapKey=clusterName
    Items []PeerRef `json:"items"`
}
```

### Updated: MirrorPeerStatus

```go
// MirrorPeerStatus defines the observed state
type MirrorPeerStatus struct {
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    Phase      PhaseType          `json:"phase,omitempty"`
    Message    PhaseMessage       `json:"message,omitempty"`
}
```

---

## High-Level Design

### 1. S3 Ownership Pattern (External S3)

**OwnerReference-based Primary/Secondary**:

When multiple MirrorPeers share external S3 (reference same secret):

```yaml
# Secret with OwnerReferences
apiVersion: v1
kind: Secret
metadata:
  name: shared-s3-secret
  namespace: openshift-dr-system
  ownerReferences:
  - apiVersion: multicluster.odf.openshift.io/v1alpha1
    kind: MirrorPeer
    name: odf-mirrorpeer          # First one - PRIMARY
    uid: abc-123
    controller: true               # Primary has controller=true
  - apiVersion: multicluster.odf.openshift.io/v1alpha1
    kind: MirrorPeer
    name: dell-mirrorpeer          # Second one - SECONDARY
    uid: def-456
    controller: false              # Secondary has controller=false
type: Opaque
data:
  AWS_ACCESS_KEY_ID: ...
  AWS_SECRET_ACCESS_KEY: ...
  s3Bucket: ...
  s3Endpoint: ...
  s3Region: ...
```

**Primary Responsibilities**:
- Propagates S3 secret to spoke clusters
- Creates S3 profile in Ramen ConfigMap
- Updates DRCluster with S3 profile reference

**Secondary Behavior**:
- Does NOT propagate S3 secret (primary already did)
- Does NOT create S3 profile (primary already did)
- Uses existing S3 configuration from primary

**Promotion on Deletion**:
When primary MirrorPeer is deleted:
1. Controller detects primary is being deleted
2. Finds all secondary owners (non-controller OwnerReferences)
3. Promotes highest-priority secondary to primary (updates OwnerReference controller=true)
4. New primary takes over S3 configuration management

**Priority for promotion** (highest to lowest):
1. Creation timestamp (older MirrorPeer)
2. Alphabetical name

---

### 2. S3 Configuration Per Cluster

**Per-Cluster Status Tracking**:

```yaml
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-mirrorpeer
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef:
      name: ocs-storagecluster
    s3SecretRef:
      name: external-s3-secret
  - clusterName: cluster2
    storageClusterRef:
      name: ocs-storagecluster
    s3SecretRef:
      name: external-s3-secret  # Same secret = shared S3
      namespace: openshift-dr-system
# Controller will:
# 1. Add controller OwnerReference to external-s3-secret (primary)
# 2. Create S3 profile in Ramen ConfigMap: s3profile-<hash>
# 3. Set DRCluster labels for both clusters
```

---

### 3. Internal S3 (ODF Only)

**Scenario 1: ODF Created First with Internal S3**

When ODF MirrorPeer is the first to configure DR:
- ODF creates internal S3 (OBC/Noobaa on each spoke)
- ODF controller generates secret from OBC credentials
- Other vendors (CNSA/Dell/Flash) created later **MUST** reference ODF's S3
- They do this by setting `s3SecretRef` to point to ODF-generated secret

Example:
```yaml
# First: ODF with internal S3
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-internal
  namespace: openshift-dr-system
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
    # No s3SecretRef - ODF creates internal S3 for this cluster
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
    # No s3SecretRef - ODF creates internal S3 for this cluster

# ODF controller will:
# 1. Create OBC on each cluster
# 2. Generate secrets in managedCluster namespace (openshift-storage):
#    - odf-internal-s3-cluster1
#    - odf-internal-s3-cluster2
# 3. Update DRCluster labels for each cluster

---
# Second: Dell must reference ODF's S3 secrets
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-dr
  namespace: openshift-dr-system
spec:
  vendor: dell
  items:
  - clusterName: cluster1
    storageClusterRef: {name: powerstore}
    s3SecretRef:
      name: odf-internal-s3-cluster1
      namespace: openshift-storage  # ← ODF created in managedCluster namespace
  - clusterName: cluster2
    storageClusterRef: {name: powerstore}
    s3SecretRef:
      name: odf-internal-s3-cluster2
      namespace: openshift-storage
```

**Constraint**: If user wants to disable ODF DR when other vendors depend on ODF's internal S3:
1. User must remove all other vendor MirrorPeers first
2. Then remove ODF MirrorPeer
3. This ensures no orphaned S3 dependencies

---

**Scenario 2: ODF Created Second (External S3 Already Exists)**

When another vendor creates DR first with external S3:
- CNSA/Dell/Flash created first, provides external S3
- ODF created later **MUST** use that external S3
- ODF sets `s3SecretRef` to reference existing secret (NOT create OBC)

Example:
```yaml
# First: CNSA with external S3
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: cnsa-dr
  namespace: openshift-dr-system
spec:
  vendor: cnsa
  items:
  - clusterName: cluster1
    storageClusterRef: {name: cnsa-storage}
    s3SecretRef:
      name: external-s3-secret  # User-provided external S3
  - clusterName: cluster2
    storageClusterRef: {name: cnsa-storage}
    s3SecretRef:
      name: external-s3-secret  # Same secret = shared S3
      namespace: openshift-dr-system

---
# Second: ODF must share CNSA's external S3
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-external
  namespace: openshift-dr-system
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
    s3SecretRef:
      name: external-s3-secret  # Same as CNSA - skips OBC creation
      namespace: openshift-dr-system
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
    s3SecretRef:
      name: external-s3-secret  # Same as CNSA - skips OBC creation
      namespace: openshift-dr-system
```

**Key Point**: ODF with `s3SecretRef` provided = external S3 mode (skips OBC creation)

---

### 4. Validation Rules

**Webhook Validations**:

1. **Vendor Immutability**: `spec.vendor` cannot change after creation
2. **S3SecretRef Immutability**: `s3SecretRef` (per PeerRef) cannot change after set
3. **Unique (Cluster, Vendor)**: Each (managedCluster, vendor) in only one MirrorPeer
4. **S3 Must Match Existing**: 
   - Check DRCluster labels for the managedCluster
   - If labels exist: `s3-secret-name` and `s3-secret-namespace`
   - New MirrorPeer's s3SecretRef MUST match exactly (both name and namespace)
   - Error: "Cluster 'X' already has S3 configured with secret 'namespace/name'. Must use same secret."
5. **Vendor-Specific S3**:
   - ODF: s3SecretRef optional (creates internal S3 if not provided)
   - CNSA/Dell/Flash: s3SecretRef required for each PeerRef
6. **Secret Must Exist**: Validate that referenced secret exists on hub cluster

---

### 5. Controller Workflow

**MirrorPeer Controller Reconciliation**:

1. **Determine S3 Configuration**:
   - If `s3SecretRef` provided → External S3
   - If vendor=odf and no `s3SecretRef` → Internal S3 (ODF creates OBC)
   - If vendor≠odf and no `s3SecretRef` → Error

2. **Check if Cluster Already Has S3**:
   - For each PeerRef, check DRCluster labels:
     - `multicluster.odf.openshift.io/s3-secret-name`
     - `multicluster.odf.openshift.io/s3-secret-namespace`
   - If labels exist AND provided s3SecretRef doesn't match → Error
   - If labels exist AND s3SecretRef matches → Reuse existing S3 profile
   - If no labels → First time configuring S3 for this cluster

3. **External S3 - Establish Ownership**:
   - Read secret referenced by `s3SecretRef`
   - Check secret's `ownerReferences`
   - If no controller owner → Make this MirrorPeer primary (add controller OwnerRef)
   - If controller owner exists → Make this MirrorPeer secondary (add non-controller OwnerRef)

4. **Primary MirrorPeer Actions**:
   - Create S3 profile in Ramen ConfigMap (generate immutable name: s3profile-<hash>)
   - Propagate S3 secret to spoke clusters (create spoke secret via ManifestWork)
   - Update/create DRCluster on each spoke with S3 profile reference
   - Set DRCluster labels: s3-secret-name and s3-secret-namespace

5. **Secondary MirrorPeer Actions**:
   - Verify S3 profile exists in Ramen ConfigMap (from primary)
   - Reference existing S3 configuration in DRCluster (already configured by primary)
   - Add self as owner to DRCluster (non-controller OwnerReference)

6. **Internal S3 - ODF Only**:
   - ODF controller creates OBC per cluster
   - ODF controller generates secret from OBC in managedCluster namespace
   - MirrorPeer controller reads generated secret
   - Creates S3 profile in Ramen ConfigMap
   - Sets DRCluster labels with secret name/namespace

7. **Deletion - Primary Promotion**:
   - If deleting MirrorPeer that is primary (controller OwnerReference on secret):
     - Find all secondary owners from secret's OwnerReferences
     - Promote oldest secondary to primary (update OwnerRef controller=true)
     - New primary reconciles and takes over S3 management
   - Remove DRCluster labels only if this is the last MirrorPeer using the cluster

---

### 6. DRCluster Ownership

**OwnerReference from MirrorPeer to DRCluster**:

Each DRCluster is owned by all MirrorPeers that reference its ManagedCluster:

```yaml
apiVersion: ramendr.openshift.io/v1alpha1
kind: DRCluster
metadata:
  name: cluster1
  ownerReferences:
  - apiVersion: multicluster.odf.openshift.io/v1alpha1
    kind: MirrorPeer
    name: odf-mirrorpeer
    uid: abc-123
    controller: false
  - apiVersion: multicluster.odf.openshift.io/v1alpha1
    kind: MirrorPeer
    name: dell-mirrorpeer
    uid: def-456
    controller: false
spec:
  s3ProfileName: s3profile-abc    # Immutable, set by first MirrorPeer
  region: us-east-1
  # Merged configuration from all owning MirrorPeers
```

**DRCluster Merging**:
- Multiple MirrorPeers (different vendors) can reference same cluster
- Each adds itself as owner via OwnerReference
- S3 profile name set by first MirrorPeer (immutable)
- Vendor-specific config merged from all owners

---

## Migration Path

**Phase 1: API Update (v1alpha1)**
- Add `vendor` field to MirrorPeerSpec (enum: odf, cnsa, dell, flash; default: odf)
- Add `SecretReference` type (name + namespace)
- Add `s3SecretRef` field to PeerRef (optional for ODF, required for others)
- Update StorageClusterRef comments (works for all vendors despite name)
- Deploy CRD with backward compatibility

**Phase 2: Controller Implementation**
- Implement OwnerReference-based primary/secondary pattern for S3 secrets
- Add DRCluster label management (s3-secret-name, s3-secret-namespace)
- Implement validation webhook (enforce s3SecretRef matching for same cluster)
- Refactor controller to support vendor abstraction
- Implement external S3 ownership logic

**Phase 3: Multi-Vendor Support**
- Test ODF internal S3 with secondary MirrorPeers
- Implement vendor-specific S3 profile generation
- Test multi-vendor scenarios (ODF + Dell, CNSA + ODF, etc.)

---

## Day 2: Identifying S3 Configuration Status (UI Support)

**Question**: How can UI easily/quickly identify if a managedCluster is already configured with S3?

**Answer**: Use DRCluster labels for fast, efficient lookups

### Recommended Approach: DRCluster Labels

MirrorPeer controller sets labels on DRCluster when configuring S3:

```yaml
apiVersion: ramendr.openshift.io/v1alpha1
kind: DRCluster
metadata:
  name: cluster1
  labels:
    multicluster.odf.openshift.io/s3-secret-name: "external-s3-secret"
    multicluster.odf.openshift.io/s3-secret-namespace: "openshift-dr-system"
```

These labels allow UI to:
1. Check if cluster has S3 configured (label exists = configured)
2. Identify which secret is being used (name + namespace)
3. Enforce constraint: second MirrorPeer must use same secret

### UI Queries

**Check if specific cluster has S3**:
```bash
kubectl get drcluster cluster1 -o jsonpath='{.metadata.labels.multicluster\.odf\.openshift\.io/s3-secret-name}'
# Returns: "external-s3-secret" or empty (if not configured)
```

**Get S3 secret namespace for cluster**:
```bash
kubectl get drcluster cluster1 -o jsonpath='{.metadata.labels.multicluster\.odf\.openshift\.io/s3-secret-namespace}'
# Returns: "openshift-dr-system" or empty
```

**List all clusters with S3 configured**:
```bash
kubectl get drcluster -l multicluster.odf.openshift.io/s3-secret-name
```

### UI Implementation Example

```javascript
// Check if cluster has S3 and get secret reference
async function getClusterS3Secret(clusterName) {
  const drCluster = await k8s.get('drcluster', clusterName);
  const labels = drCluster?.metadata?.labels || {};
  
  const secretName = labels['multicluster.odf.openshift.io/s3-secret-name'];
  const secretNamespace = labels['multicluster.odf.openshift.io/s3-secret-namespace'];
  
  if (!secretName || !secretNamespace) {
    return null;  // No S3 configured
  }
  
  return {
    name: secretName,
    namespace: secretNamespace
  };
}

// List all clusters with S3 (for dropdown filtering)
async function listClustersWithS3() {
  return await k8s.list('drcluster', {
    labelSelector: 'multicluster.odf.openshift.io/s3-secret-name'
  });
}

// UI Form Validation: Enforce same secret for second MirrorPeer
async function validateNewMirrorPeer(clusters, s3SecretRef) {
  for (const cluster of clusters) {
    const existingSecret = await getClusterS3Secret(cluster);
    
    if (existingSecret) {
      // Cluster already has S3 - new MirrorPeer must use same secret
      if (!s3SecretRef) {
        throw new Error(
          `Cluster ${cluster} already has S3 configured with secret ${existingSecret.namespace}/${existingSecret.name}. ` +
          `You must specify the same s3SecretRef.`
        );
      }
      
      if (s3SecretRef.name !== existingSecret.name || 
          s3SecretRef.namespace !== existingSecret.namespace) {
        throw new Error(
          `Cluster ${cluster} already has S3 configured with secret ${existingSecret.namespace}/${existingSecret.name}. ` +
          `Cannot use different secret ${s3SecretRef.namespace}/${s3SecretRef.name}.`
        );
      }
    }
  }
}
```

### Benefits

✅ **Fast**: Single API call per cluster (no iteration)  
✅ **Persistent**: Labels survive even if MirrorPeer is deleted  
✅ **Authoritative**: DRCluster is where S3 is actually used  
✅ **Simple**: Direct label queries, no aggregation needed  
✅ **List Support**: Can efficiently list all configured clusters  

### Alternative: Check MirrorPeer Spec

If DRCluster labels are not available, fallback to checking MirrorPeer specs:

```javascript
async function getClusterS3SecretFromMirrorPeers(clusterName) {
  const mirrorPeers = await k8s.list('mirrorpeer', {allNamespaces: true});
  
  for (const mp of mirrorPeers.items) {
    for (const peerRef of mp.spec.items || []) {
      if (peerRef.clusterName === clusterName && peerRef.s3SecretRef) {
        return {
          name: peerRef.s3SecretRef.name,
          namespace: peerRef.s3SecretRef.namespace,
          configuredBy: mp.metadata.name
        };
      }
    }
  }
  
  return null;  // No S3 configured
}
```

**Trade-off**: Requires iterating all MirrorPeers (slower than label query, but can identify which MirrorPeer configured S3)

---

## Benefits

✅ **Clean Ownership Model**: OwnerReferences clearly identify primary vs secondary  
✅ **Automatic Failover**: Secondary auto-promoted when primary deleted  
✅ **Immutable S3 Profile**: Once set, profile name never changes  
✅ **Per-Cluster Visibility**: Status shows exact S3 config per cluster  
✅ **Namespace-Scoped Secrets**: S3 secrets in MirrorPeer namespace (clear RBAC)  
✅ **Backward Compatible**: Existing MirrorPeers work without changes  
✅ **Multi-Vendor Ready**: Single cluster can have multiple vendors with shared S3  

---

## Open Items - RESOLVED ✅

~~1. **Internal S3 Multi-MirrorPeer**~~  
**RESOLVED**: 
- If ODF created first with internal S3: Other vendors reference ODF's generated secret
- If ODF created second: ODF must use existing external S3 (set s3SecretRef)
- Constraint: To remove ODF with internal S3, must remove all dependent MirrorPeers first

~~2. **S3 Migration**~~  
**RESOLVED**: 
- Internal → External: Not supported. User must delete all DR config and recreate
- This is acceptable as it's a major infrastructure change

~~3. **Priority Customization**~~  
**RESOLVED**: 
- NO custom priority field
- First MirrorPeer to reference secret becomes primary (controller owner)
- Promotion on deletion: Oldest remaining secondary becomes primary (by creation timestamp)

~~4. **Day 2 S3 Status Query**~~  
**RESOLVED**: 
- Use DRCluster labels: `multicluster.odf.openshift.io/s3-secret-name` and `s3-secret-namespace`
- Fast single-query lookup for UI
- Fallback: Iterate MirrorPeer specs to find s3SecretRef for cluster

---

## Implementation Summary

### API Changes Required

1. **Add to `mirrorpeer_types.go`**:
   - `StorageVendor` type and constants (odf, cnsa, dell, flash)
   - `SecretReference` type with `Name` and `Namespace` fields
   - `Vendor` field in `MirrorPeerSpec` (default: odf, immutable)
   - `S3SecretRef` field in `PeerRef` (type: *SecretReference, optional for ODF, required for others)

2. **Update Comments**:
   - `StorageClusterRef`: Works for all vendors despite the name

3. **Add Validation (Webhook)**:
   - CEL: vendor immutable
   - CEL: s3SecretRef immutable (per PeerRef)
   - Webhook: Validate s3SecretRef matches existing DRCluster labels (if any)
   - Webhook: Validate secret exists
   - Webhook: Vendor-specific validation (CNSA/Dell/Flash require s3SecretRef)

### Controller Changes Required

1. **S3 Ownership Management**:
   - Check secret's OwnerReferences to determine primary vs secondary
   - Add controller OwnerReference if primary
   - Add non-controller OwnerReference if secondary

2. **Primary Promotion on Deletion**:
   - Finalizer on MirrorPeer
   - On deletion: if primary role, promote oldest secondary
   - Update secret's OwnerReferences (change controller flag)

3. **DRCluster Label Management**:
   - Set labels when configuring S3:
     - `multicluster.odf.openshift.io/s3-secret-name: "<secret-name>"`
     - `multicluster.odf.openshift.io/s3-secret-namespace: "<namespace>"`
   - Remove labels only when last MirrorPeer using the cluster is deleted

4. **S3 Profile Name Generation**:
   - Generate immutable profile name: `s3profile-<hash-of-secret-ref>`
   - Store in Ramen ConfigMap
   - Reference in DRCluster spec.s3ProfileName

5. **ODF Internal S3 Handling**:
   - If vendor=odf and no s3SecretRef: create OBC, generate secret
   - If vendor=odf and s3SecretRef provided: skip OBC, use external
   - Generated secret name format: `<mirrorpeer-name>-s3-<cluster-name>`

### Webhook Changes Required

1. **Validating Webhook**:
   - Vendor immutability (already in CEL)
   - S3SecretRef immutability per PeerRef (already in CEL)
   - Unique (cluster, vendor) pairs across all MirrorPeers
   - S3 constraint: If cluster has S3 (check DRCluster label), new PeerRef for that cluster cannot provide s3SecretRef
   - Vendor-specific: CNSA/Dell/Flash require s3SecretRef on each PeerRef
   - Per-cluster validation: Each PeerRef must have s3SecretRef for non-ODF vendors

2. **No Mutating Webhook Needed**:
   - Defaults handled by kubebuilder annotations
   - No auto-conversion needed

### Testing Scenarios

1. **ODF Internal First**:
   - Create ODF MirrorPeer without s3SecretRef
   - Verify OBC created, secret generated in managedCluster namespace
   - Verify DRCluster labels set (s3-secret-name, s3-secret-namespace)
   - Create Dell MirrorPeer with s3SecretRef pointing to ODF secret
   - Verify Dell MirrorPeer has non-controller OwnerRef on ODF secret
   - Verify both MirrorPeers use same S3 profile in Ramen ConfigMap

2. **External S3 First**:
   - Create CNSA MirrorPeer with external s3SecretRef
   - Verify secret has controller OwnerReference (CNSA is primary)
   - Verify DRCluster labels set for both clusters
   - Create ODF MirrorPeer with same s3SecretRef
   - Verify ODF has non-controller OwnerRef on secret (secondary)
   - Verify ODF skips OBC creation

3. **Primary Deletion and Promotion**:
   - Create 3 MirrorPeers all referencing same external S3
   - Verify first has controller OwnerRef (primary), others don't (secondary)
   - Delete primary MirrorPeer
   - Verify oldest secondary promoted to primary (controller OwnerRef updated)
   - Verify DRCluster labels remain intact

4. **DRCluster Labels for UI**:
   - Create MirrorPeer with S3
   - Verify DRCluster has labels: s3-secret-name and s3-secret-namespace
   - Query DRCluster by label selector
   - Verify UI can detect S3 status with single API call

5. **Validation: Enforce Same Secret**:
   - Create first MirrorPeer with s3SecretRef=secretA
   - Verify DRCluster labels set
   - Attempt to create second MirrorPeer with s3SecretRef=secretB
   - Verify webhook rejects with error about mismatched secret

### Migration for Existing Deployments

1. **Existing ODF MirrorPeers**:
   - Automatically get vendor=odf (default)
   - Continue working without s3SecretRef (internal S3)
   - DRCluster gets labels on next reconcile
   - OwnerReferences added to existing resources

2. **No Breaking Changes**:
   - All new fields are optional or have defaults
   - vendor defaults to "odf"
   - s3SecretRef is optional for ODF
   - StorageClusterRef works as before
   - Existing behavior preserved

### Rollout Plan

**Phase 1**: API and CRD
- Update types
- Generate CRD
- Deploy to test environment
- Verify existing MirrorPeers still work

**Phase 2**: Controller (S3 ownership)
- Implement OwnerReference logic
- Implement primary/secondary determination
- Implement status tracking
- Deploy and test with external S3

**Phase 3**: Controller (DRCluster labels)
- Add label management
- Test UI queries
- Verify fast lookups

**Phase 4**: Webhooks
- Implement validation
- Test constraint enforcement
- Test with multiple vendors

**Phase 5**: ODF Controller Updates
- Update to respect s3SecretRef
- Skip OBC when external S3 used
- Generate secrets for internal S3

---

## Final API Example

```yaml
# Example 1: ODF with internal S3 (first backend)
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-internal
  namespace: openshift-dr-system
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
  # No s3SecretRef - ODF will create internal S3

# ODF controller creates:
# - OBC on each cluster (cluster1, cluster2)
# - Secrets in openshift-storage namespace:
#   - odf-internal-s3-cluster1
#   - odf-internal-s3-cluster2
# - DRCluster labels for each cluster

---
# Example 2: Dell referencing ODF's internal S3
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-secondary
  namespace: openshift-dr-system
spec:
  vendor: dell
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef: {name: powerstore}
    s3SecretRef:
      name: odf-internal-s3-cluster1  # References ODF's secret for cluster1
      namespace: openshift-storage
  - clusterName: cluster2
    storageClusterRef: {name: powerstore}
    s3SecretRef:
      name: odf-internal-s3-cluster2  # References ODF's secret for cluster2
      namespace: openshift-storage

# Dell controller:
# - Adds non-controller OwnerReference to ODF secrets
# - Reuses same S3 profile from Ramen ConfigMap
# - Adds self as owner to DRCluster (non-controller)

---
# Example 3: External S3 shared by multiple vendors
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: cnsa-primary
  namespace: openshift-dr-system
  creationTimestamp: "2024-01-01T00:00:00Z"
spec:
  vendor: cnsa
  type: async
  items:
  - clusterName: cluster3
    storageClusterRef: {name: cnsa-storage}
    s3SecretRef:
      name: external-s3  # Both clusters share same S3
  - clusterName: cluster4
    storageClusterRef: {name: cnsa-storage}
    s3SecretRef:
      name: external-s3  # Both clusters share same S3
      namespace: openshift-dr-system

# CNSA controller (created first, becomes primary):
# - Adds controller OwnerReference to external-s3 secret
# - Creates S3 profile in Ramen ConfigMap: s3profile-<hash>
# - Sets DRCluster labels for both clusters

---
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-secondary
  namespace: openshift-dr-system
  creationTimestamp: "2024-01-02T00:00:00Z"  # Created later
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster3
    storageClusterRef: {name: ocs-storagecluster}
    s3SecretRef:
      name: external-s3  # Same as CNSA - skips OBC
      namespace: openshift-dr-system
  - clusterName: cluster4
    storageClusterRef: {name: ocs-storagecluster}
    s3SecretRef:
      name: external-s3  # Same as CNSA - skips OBC
      namespace: openshift-dr-system

# ODF controller (created second, becomes secondary):
# - Adds non-controller OwnerReference to external-s3 secret
# - Reuses CNSA's S3 profile from Ramen ConfigMap
# - Skips OBC creation (s3SecretRef provided)
# - Adds self as owner to DRCluster (non-controller)
```
