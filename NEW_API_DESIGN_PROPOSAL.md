# MirrorPeer API Redesign - Separation of Concerns

## Executive Summary

Decouple S3 profile management from MirrorPeer by:
1. **Creating a new `S3Profile` CR** for managing S3 configuration independently
2. **MirrorPeer has NO S3 fields** - S3Profile is auto-discovered by cluster name
3. **S3Profile handles all S3 concerns** - OBC creation, Ramen ConfigMap updates, secret propagation
4. **One S3Profile can serve multiple DRClusters** (one-to-many relationship)

---

## Current State

The current MirrorPeer API has:
- `manageS3` field to determine which MirrorPeer manages S3
- S3 configuration is tightly coupled with MirrorPeer reconciliation
- Complex coordination needed when multiple MirrorPeers exist

**Issues with Current Approach:**
- S3 is infrastructure that should be managed independently
- S3 lifecycle is coupled with MirrorPeer lifecycle
- Complex coordination logic needed (which MirrorPeer "wins" S3 management)
- Difficult to update S3 configuration independently
- Cannot easily reuse same S3 configuration across multiple vendors/MirrorPeers
- The `manageS3` field creates confusion about ownership

---

## Proposed Solution: Separate S3Profile CR

### Architecture

```
┌─────────────────┐
│   S3Profile     │ ◄─── Manages S3 configuration
│  (New CR)       │      - Creates S3 profile in Ramen ConfigMap
└────────┬────────┘      - Propagates secrets to spoke clusters
         │
         │ referenced by
         │
┌────────▼────────┐
│   MirrorPeer    │ ◄─── Only owns DRClusters
│                 │      - References S3Profile by name
└────────┬────────┘      - Manages storage-level peering
         │
         │ owns
         │
┌────────▼────────┐
│   DRCluster     │ ◄─── References S3ProfileName
│  (on spoke)     │      - One S3Profile per DRCluster
└─────────────────┘      - Multiple DRClusters can share same S3Profile
```

---

## New API Design

### 1. New CR: S3Profile

```go
package v1alpha1

// S3Profile manages S3 configuration for DR metadata storage
// One S3Profile can be referenced by multiple DRClusters
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type S3Profile struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   S3ProfileSpec   `json:"spec,omitempty"`
    Status S3ProfileStatus `json:"status,omitempty"`
}

// S3ProfileSpec defines the desired S3 configuration
type S3ProfileSpec struct {
    // InternalS3 specifies configuration for ODF-managed internal S3 (Noobaa/RGW).
    // When specified, the S3Profile controller will:
    //   1. Use addon mechanism to create OBC on the specified cluster (internalS3.managedCluster)
    //   2. Wait for addon to transfer generated secret from spoke to hub
    //   3. Copy S3 secret to Ramen operator namespace on hub
    //   4. Use the secret to configure Ramen ConfigMap S3 profile
    //   5. Create DRCluster on hub for all clusters in spec.managedClusters
    // 
    // This field is mutually exclusive with ExternalS3.
    // Only valid when used with ODF storage.
    // 
    // +kubebuilder:validation:Optional
    InternalS3 *InternalS3Spec `json:"internalS3,omitempty"`
    
    // ExternalS3 specifies configuration for external S3-compatible storage.
    // When specified, the referenced secret must already exist on the hub.
    // Use this for:
    //   - Vendor-provided S3 (CNSA, Dell, Flash)
    //   - External S3 services (AWS S3, MinIO, etc.)
    //   - ODF with external S3 endpoint
    // 
    // This field is mutually exclusive with InternalS3.
    // 
    // +kubebuilder:validation:Optional
    ExternalS3 *ExternalS3Spec `json:"externalS3,omitempty"`
    
    // ManagedClusters is the list of clusters where this S3 profile will be used.
    // A DRCluster will be created on the hub for each cluster in this list.
    // 
    // For InternalS3: 
    //   - OBC created on ONE cluster (specified in internalS3.managedCluster)
    //   - S3 secret copied to Ramen operator namespace on hub
    //   - DRCluster created on hub for ALL clusters in this list
    //   - All DRClusters reference the S3 endpoint from the cluster where OBC was created
    // 
    // For ExternalS3: 
    //   - S3 secret copied to Ramen operator namespace on hub
    //   - DRCluster created on hub for ALL clusters in this list
    //   - All DRClusters reference the same external S3 endpoint
    // 
    // IMPORTANT: 
    //   - Each cluster can only appear in ONE S3Profile's managedClusters list (enforced by webhook)
    //   - DRCluster is a hub-only API, not deployed to spoke clusters
    // 
    // The S3Profile CR name will be used as the s3ProfileName in Ramen ConfigMap
    // and in DRCluster.spec.s3ProfileName.
    // 
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    ManagedClusters []string `json:"managedClusters"`
}

// InternalS3Spec defines configuration for ODF-managed internal S3
type InternalS3Spec struct {
    // ManagedCluster is the name of the cluster where the OBC will be created.
    // This cluster must be one of the clusters in spec.managedClusters.
    // 
    // The OBC will ONLY be created on this cluster, and the generated S3 endpoint
    // will be shared by all clusters in spec.managedClusters.
    // 
    // Example:
    //   spec.managedClusters: [cluster1, cluster2, cluster3]
    //   internalS3.managedCluster: cluster1
    // Result: OBC created only on cluster1, but cluster1/cluster2/cluster3 all use that S3
    // 
    // +kubebuilder:validation:Required
    ManagedCluster string `json:"managedCluster"`
    
    // StorageClassName is the name of the StorageClass to use for OBC creation.
    // This StorageClass should be provided by the ODF operator on the managed cluster.
    // Examples: "openshift-storage.noobaa.io", "ocs-storagecluster-ceph-rgw"
    // 
    // +kubebuilder:validation:Required
    StorageClassName string `json:"storageClassName"`
    
    // Namespace is the namespace where OBC will be created on the managed cluster.
    // Typically "openshift-storage" for ODF deployments.
    // 
    // +kubebuilder:validation:Required
    Namespace string `json:"namespace"`
    
    // OBCName is the name to use for the ObjectBucketClaim.
    // If not specified, a name will be generated: "odr-<s3profile-cr-name>"
    // 
    // +kubebuilder:validation:Optional
    OBCName string `json:"obcName,omitempty"`
}

// ExternalS3Spec defines configuration for external S3-compatible storage
type ExternalS3Spec struct {
    // SecretRef references a secret containing S3 credentials.
    // The secret must exist on the hub cluster in the specified namespace and contain:
    //   - AWS_ACCESS_KEY_ID: S3 access key
    //   - AWS_SECRET_ACCESS_KEY: S3 secret key
    //   - s3Bucket: Bucket name for DR metadata
    //   - s3Endpoint: S3 endpoint URL
    //   - s3Region: S3 region (optional, defaults to us-east-1)
    // 
    // +kubebuilder:validation:Required
    SecretRef SecretReference `json:"secretRef"`
}

// S3ProfileStatus defines the observed state of S3Profile
type S3ProfileStatus struct {
    // Conditions represent the latest available observations of the S3Profile's state
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    
    // Phase represents the current phase of S3Profile
    Phase S3ProfilePhase `json:"phase,omitempty"`
    
    // Message provides additional information about the current phase
    Message string `json:"message,omitempty"`
    
    // ConfiguredClusters lists clusters where S3 has been successfully configured
    ConfiguredClusters []string `json:"configuredClusters,omitempty"`
}

// S3ProfilePhase represents the lifecycle phase of S3Profile
// +kubebuilder:validation:Enum=Pending;Configuring;Ready;Failed
type S3ProfilePhase string

const (
    S3ProfilePhasePending     S3ProfilePhase = "Pending"
    S3ProfilePhaseConfiguring S3ProfilePhase = "Configuring"
    S3ProfilePhaseReady       S3ProfilePhase = "Ready"
    S3ProfilePhaseFailed      S3ProfilePhase = "Failed"
)
```

### 2. Updated MirrorPeer API (No S3 Fields)

```go
// PeerRef holds a reference to a mirror peer
type PeerRef struct {
    // ClusterName is the name of ManagedCluster
    // 
    // The S3Profile for this cluster is automatically discovered by finding
    // which S3Profile has this cluster in its managedClusters list.
    // 
    // +kubebuilder:validation:Required
    ClusterName string `json:"clusterName"`
    
    // StorageClusterRef holds a reference to the storage resource on this cluster
    // Despite the name, this works for all storage vendors
    // +kubebuilder:validation:Required
    StorageClusterRef StorageClusterRef `json:"storageClusterRef"`
}

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

---

## Workflow

### Workflow 1: Create S3 Profile (External S3)

```yaml
# Step 1: Create S3 Secret (user provides)
apiVersion: v1
kind: Secret
metadata:
  name: external-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "AKIAIOSFODNN7EXAMPLE"
  AWS_SECRET_ACCESS_KEY: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
  s3Bucket: "my-dr-metadata-bucket"
  s3Endpoint: "https://s3.amazonaws.com"
  s3Region: "us-west-2"

---
# Step 2: Create S3Profile CR
# IMPORTANT: CR name IS the s3ProfileName that will be used in Ramen ConfigMap
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: s3profile-aws-prod  # This name will be used as s3ProfileName
spec:
  externalS3:
    secretRef:
      name: external-s3-secret
      namespace: openshift-dr-system
  managedClusters:
  - cluster1
  - cluster2
  - cluster3

# S3Profile controller will:
# 1. Read external S3 secret
# 2. Validate the secret has required keys
# 3. Copy S3 secret to Ramen operator namespace on hub (e.g., openshift-dr-system)
# 4. Create S3 profile in Ramen ConfigMap with name "s3profile-aws-prod" (CR name)
# 5. Create/update DRCluster on hub for cluster1, cluster2, cluster3
#    - Set spec.s3ProfileName = "s3profile-aws-prod"
#    - DRCluster is hub-only, not deployed to spokes
# 6. Set status.phase = Ready
```

### Workflow 2: Create MirrorPeer (S3Profile auto-discovered)

```yaml
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-async-dr
spec:
  vendor: odf
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    # No s3ProfileName needed! Auto-discovered from S3Profile
  - clusterName: cluster2
    storageClusterRef:
      name: ocs-storagecluster
      namespace: openshift-storage
    # No s3ProfileName needed! Auto-discovered from S3Profile

# MirrorPeer controller will:
# 1. For each cluster, look up which S3Profile has it in managedClusters list
#    - cluster1 → finds S3Profile "s3profile-aws-prod"
#    - cluster2 → finds S3Profile "s3profile-aws-prod"
# 2. Validate that both clusters found an S3Profile
# 3. Validate that S3Profiles are Ready
# 4. Setup vendor-specific peering (ODF Mirroring in this case)
# 5. Update DRCluster on hub for each cluster:
#    - DRCluster already created by S3Profile controller
#    - Add MirrorPeer as owner (OwnerReference)
#    - Add vendor-specific configuration
# 6. Set status.phase = Ready
```

### Workflow 3: Reuse S3Profile across multiple vendors

```yaml
# Same S3Profile can be used by multiple MirrorPeers with different vendors
# S3Profile automatically discovered by cluster name
---
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

---
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: dell-dr
spec:
  vendor: dell
  type: async
  items:
  - clusterName: cluster1
    storageClusterRef: {name: powerstore}
  - clusterName: cluster2
    storageClusterRef: {name: powerstore}

# Result:
# - Both MirrorPeers auto-discover S3Profile "s3profile-aws-prod" (has cluster1, cluster2)
# - cluster1 DRCluster has s3ProfileName = "s3profile-aws-prod", shared by both vendors
# - cluster2 DRCluster has s3ProfileName = "s3profile-aws-prod", shared by both vendors
```

---

## Controller Responsibilities

### S3Profile Controller

**Responsibilities:**
1. **Handle Internal S3 (ODF)**: 
   - Create OBC on specified cluster via addon mechanism
   - Wait for addon to transfer generated secret to hub
2. **Handle External S3**:
   - Validate referenced secret exists and has required keys
3. **Copy S3 Secret to Ramen Namespace**:
   - Copy/create S3 secret in Ramen operator namespace on hub (e.g., `openshift-dr-system`)
   - This is where Ramen operator will read the secret from
4. **Create S3 Profile in Ramen ConfigMap**: 
   - Profile name: S3Profile CR name
   - Add to Ramen ConfigMap's `s3StoreProfiles` section
   - ConfigMap is in Ramen operator namespace on hub
5. **Create/Update DRCluster on Hub**:
   - For each cluster in `spec.managedClusters`
   - Create/update DRCluster CR on **hub cluster**
   - Set `spec.s3ProfileName` to S3Profile CR name
   - DRCluster is a hub-only API, not deployed to spokes
6. **Status Management**:
   - Track which clusters have been successfully configured
   - Update `status.configuredClusters`

**Reconciliation Logic:**
```go
func (r *S3ProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    s3Profile := &multiclusterv1alpha1.S3Profile{}
    if err := r.Get(ctx, req.NamespacedName, s3Profile); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // Determine S3 type and get secret
    var secret *corev1.Secret
    var err error
    
    switch {
    case s3Profile.Spec.InternalS3 != nil:
        // ODF Internal S3: Create OBC via addon and get generated secret
        secret, err = r.reconcileInternalS3(ctx, s3Profile)
        if err != nil {
            return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
                fmt.Sprintf("Failed to configure internal S3: %s", err))
        }
        if secret == nil {
            // Still waiting for OBC to generate secret
            return r.updateStatus(ctx, s3Profile, S3ProfilePhaseConfiguring,
                "Waiting for OBC to be created and secret generated")
        }
        
    case s3Profile.Spec.ExternalS3 != nil:
        // External S3: Get existing secret
        secret, err = r.getExternalS3Secret(ctx, s3Profile.Spec.ExternalS3.SecretRef)
        if err != nil {
            return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
                fmt.Sprintf("Failed to get external S3 secret: %s", err))
        }
        
    default:
        return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
            "Must specify one of: internalS3 or externalS3")
    }
    
    // Validate secret has required keys
    requiredKeys := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", 
                             "s3Bucket", "s3Endpoint"}
    for _, key := range requiredKeys {
        if _, ok := secret.Data[key]; !ok {
            return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
                fmt.Sprintf("S3 secret missing required key: %s", key))
        }
    }
    
    // Use S3Profile CR name as s3ProfileName
    s3ProfileName := s3Profile.Name
    
    // Copy S3 secret to Ramen operator namespace (if not already there)
    ramenNamespace := r.getRamenNamespace() // e.g., "openshift-dr-system"
    if err := r.copyS3SecretToRamenNamespace(ctx, secret, ramenNamespace); err != nil {
        return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
            fmt.Sprintf("Failed to copy S3 secret to Ramen namespace: %s", err))
    }
    
    // Create/update S3 profile in Ramen ConfigMap
    if err := r.updateRamenConfigMapS3Profile(ctx, s3ProfileName, secret, ramenNamespace); err != nil {
        return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
            fmt.Sprintf("Failed to update Ramen ConfigMap: %s", err))
    }
    
    // Create/update DRCluster on hub for each managed cluster
    var configuredClusters []string
    for _, clusterName := range s3Profile.Spec.ManagedClusters {
        // Create/update DRCluster on hub (not on spoke)
        if err := r.createOrUpdateDRCluster(ctx, clusterName, s3ProfileName); err != nil {
            return r.updateStatus(ctx, s3Profile, S3ProfilePhaseFailed,
                fmt.Sprintf("Failed to create/update DRCluster for %s: %s", clusterName, err))
        }
        
        configuredClusters = append(configuredClusters, clusterName)
    }
    
    // Update status
    s3Profile.Status.ConfiguredClusters = configuredClusters
    return r.updateStatus(ctx, s3Profile, S3ProfilePhaseReady, "S3 profile configured successfully")
}

// copyS3SecretToRamenNamespace copies the S3 secret to Ramen operator namespace
func (r *S3ProfileReconciler) copyS3SecretToRamenNamespace(
    ctx context.Context,
    sourceSecret *corev1.Secret,
    ramenNamespace string,
) error {
    targetSecret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      sourceSecret.Name,
            Namespace: ramenNamespace,
        },
        Type: sourceSecret.Type,
        Data: sourceSecret.Data,
    }
    
    // Create or update secret in Ramen namespace
    existing := &corev1.Secret{}
    err := r.Get(ctx, types.NamespacedName{
        Name:      targetSecret.Name,
        Namespace: ramenNamespace,
    }, existing)
    
    if err != nil {
        if apierrors.IsNotFound(err) {
            return r.Create(ctx, targetSecret)
        }
        return err
    }
    
    // Update existing secret
    existing.Data = targetSecret.Data
    return r.Update(ctx, existing)
}
}

// reconcileInternalS3 handles ODF internal S3 (Noobaa/RGW) via OBC
func (r *S3ProfileReconciler) reconcileInternalS3(
    ctx context.Context,
    s3Profile *multiclusterv1alpha1.S3Profile,
) (*corev1.Secret, error) {
    internalS3 := s3Profile.Spec.InternalS3
    
    obcName := internalS3.OBCName
    if obcName == "" {
        obcName = fmt.Sprintf("odr-%s", s3Profile.Name)
    }
    
    // Create OBC on the SINGLE managed cluster specified in internalS3.managedCluster
    // The generated S3 endpoint will be shared by all clusters in spec.managedClusters
    clusterName := internalS3.ManagedCluster
    
    if err := r.createOBCViaAddon(ctx, clusterName, obcName, 
        internalS3.StorageClassName, internalS3.Namespace); err != nil {
        return nil, fmt.Errorf("failed to create OBC on cluster %s: %w", clusterName, err)
    }
    
    // Wait for addon to transfer generated secret to hub
    // The addon will create a secret on the hub with name: <s3profile-name>-s3-secret
    // in the same namespace as the S3Profile CR
    hubSecretName := fmt.Sprintf("%s-s3-secret", s3Profile.Name)
    hubSecretNamespace := s3Profile.Namespace
    if hubSecretNamespace == "" {
        hubSecretNamespace = "openshift-dr-system"
    }
    
    secret := &corev1.Secret{}
    secretKey := types.NamespacedName{
        Name:      hubSecretName,
        Namespace: hubSecretNamespace,
    }
    if err := r.Get(ctx, secretKey, secret); err != nil {
        if apierrors.IsNotFound(err) {
            return nil, nil // Secret not ready yet, will requeue
        }
        return nil, err
    }
    
    return secret, nil
}

// createOBCViaAddon creates an OBC on the spoke cluster using the addon mechanism
func (r *S3ProfileReconciler) createOBCViaAddon(
    ctx context.Context,
    clusterName string,
    obcName string,
    storageClassName string,
    namespace string,
) error {
    // Create a ManifestWork to deploy OBC on spoke cluster
    // The addon on the spoke will:
    // 1. Create the OBC in the specified namespace
    // 2. Wait for it to be bound
    // 3. Extract the secret generated by OBC
    // 4. Transfer the secret to hub cluster
    
    obc := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "objectbucket.io/v1alpha1",
            "kind":       "ObjectBucketClaim",
            "metadata": map[string]interface{}{
                "name":      obcName,
                "namespace": namespace,
            },
            "spec": map[string]interface{}{
                "generateBucketName": obcName,
                "storageClassName":   storageClassName,
            },
        },
    }
    
    return r.createManifestWorkWithCallback(ctx, clusterName, obcName, obc, 
        "transfer-obc-secret") // Callback to transfer secret to hub
}

func (r *S3ProfileReconciler) getExternalS3Secret(
    ctx context.Context,
    secretRef SecretReference,
) (*corev1.Secret, error) {
    secret := &corev1.Secret{}
    secretKey := types.NamespacedName{
        Name:      secretRef.Name,
        Namespace: secretRef.Namespace,
    }
    if err := r.Get(ctx, secretKey, secret); err != nil {
        return nil, err
    }
    return secret, nil
}

func (r *S3ProfileReconciler) createOrUpdateDRCluster(
    ctx context.Context, 
    clusterName string, 
    s3ProfileName string,
) error {
    // DRCluster is a hub-only API - create/update on hub cluster
    drCluster := &ramenv1alpha1.DRCluster{
        ObjectMeta: metav1.ObjectMeta{
            Name: clusterName,
        },
        Spec: ramenv1alpha1.DRClusterSpec{
            S3ProfileName: s3ProfileName,
            // Region and other fields may be populated by MirrorPeer controller
        },
    }
    
    // Check if DRCluster already exists
    existing := &ramenv1alpha1.DRCluster{}
    err := r.Get(ctx, types.NamespacedName{Name: clusterName}, existing)
    
    if err != nil {
        if apierrors.IsNotFound(err) {
            // Create new DRCluster on hub
            return r.Create(ctx, drCluster)
        }
        return err
    }
    
    // Update existing DRCluster
    existing.Spec.S3ProfileName = s3ProfileName
    return r.Update(ctx, existing)
}
```

### MirrorPeer Controller (Simplified)

**Responsibilities (S3 removed):**
1. **Validate S3Profile exists**: Ensure referenced S3Profile CR exists
2. **Validate cluster is in S3Profile's managedClusters**: Ensure S3 is already configured
3. **Setup vendor-specific peering**: Call vendor reconciler
4. **Own DRCluster**: Add OwnerReference to DRCluster (but don't manage S3)
5. **Status management**: Track peering status only

**Reconciliation Logic:**
```go
func (r *MirrorPeerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    mp := &multiclusterv1alpha1.MirrorPeer{}
    if err := r.Get(ctx, req.NamespacedName, mp); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // Auto-discover S3Profile for each cluster
    for _, peerRef := range mp.Spec.Items {
        // Find S3Profile that has this cluster in its managedClusters list
        s3Profile, err := r.getS3ProfileForCluster(ctx, peerRef.ClusterName)
        if err != nil {
            return r.updateStatus(ctx, mp, Failed,
                fmt.Sprintf("No S3Profile found for cluster %s: %s", 
                    peerRef.ClusterName, err))
        }
        
        // Validate S3Profile is ready
        if s3Profile.Status.Phase != S3ProfilePhaseReady {
            return r.updateStatus(ctx, mp, Configuring,
                fmt.Sprintf("Waiting for S3Profile %s to be ready for cluster %s", 
                    s3Profile.Name, peerRef.ClusterName))
        }
    }
    
    // Get S3Profiles for all clusters (used later for DRCluster config)
    s3ProfileMap := make(map[string]*multiclusterv1alpha1.S3Profile)
    for _, peerRef := range mp.Spec.Items {
        s3Profile, _ := r.getS3ProfileForCluster(ctx, peerRef.ClusterName)
        s3ProfileMap[peerRef.ClusterName] = s3Profile
    }
    
    // Get vendor reconciler
    vendor := mp.Spec.Vendor
    if vendor == "" {
        vendor = StorageVendorODF
    }
    vendorReconciler, err := GetVendorReconciler(vendor, r.Client, r.Scheme)
    if err != nil {
        return r.updateStatus(ctx, mp, Failed, err.Error())
    }
    
    // Setup storage peering (vendor-specific)
    if err := vendorReconciler.ReconcilePeering(ctx, mp); err != nil {
        return r.updateStatus(ctx, mp, Failed,
            fmt.Sprintf("Peering failed: %s", err))
    }
    
    // Add MirrorPeer as owner to each DRCluster
    // (DRCluster already has s3ProfileName set by S3Profile controller)
    for _, peerRef := range mp.Spec.Items {
        s3Profile := s3ProfileMap[peerRef.ClusterName]
        if err := r.addMirrorPeerOwnerToDRCluster(ctx, mp, peerRef.ClusterName, s3Profile.Name); err != nil {
            return r.updateStatus(ctx, mp, Failed,
                fmt.Sprintf("Failed to update DRCluster for %s: %s", 
                    peerRef.ClusterName, err))
        }
    }
    
    return r.updateStatus(ctx, mp, Ready, "MirrorPeer is ready")
}

// getS3ProfileForCluster finds the S3Profile that has the cluster in its managedClusters list
func (r *MirrorPeerReconciler) getS3ProfileForCluster(
    ctx context.Context, 
    clusterName string,
) (*multiclusterv1alpha1.S3Profile, error) {
    s3ProfileList := &multiclusterv1alpha1.S3ProfileList{}
    if err := r.List(ctx, s3ProfileList); err != nil {
        return nil, err
    }
    
    for i := range s3ProfileList.Items {
        s3Profile := &s3ProfileList.Items[i]
        for _, managedCluster := range s3Profile.Spec.ManagedClusters {
            if managedCluster == clusterName {
                return s3Profile, nil
            }
        }
    }
    
    return nil, fmt.Errorf("no S3Profile found with cluster %s in managedClusters", clusterName)
}
```

---

## Benefits of This Design

### ✅ **Separation of Concerns**
- S3 management is independent of DR pairing
- S3Profile has single responsibility: manage S3 configuration
- MirrorPeer has single responsibility: manage storage peering

### ✅ **Reusability**
- One S3Profile can serve multiple DRClusters
- One S3Profile can be shared across multiple vendors (ODF, Dell, CNSA)
- Easy to create new MirrorPeers without duplicating S3 configuration

### ✅ **Simplified Lifecycle**
- Update S3 credentials: Edit S3Profile, all using DRClusters get updated
- Delete S3Profile: Can be protected if DRClusters still reference it
- Delete MirrorPeer: Doesn't affect S3 configuration

### ✅ **Clearer Ownership**
- S3Profile owns: S3 secret propagation, Ramen ConfigMap S3 profile section
- MirrorPeer owns: DRCluster, vendor-specific peering
- No complex primary/secondary ownership patterns needed

### ✅ **Better Day 2 Operations**
- Rotate S3 credentials: Update S3 secret, S3Profile controller propagates
- Add cluster to existing S3: Add to S3Profile.spec.managedClusters
- UI can list available S3Profiles for MirrorPeer creation

### ✅ **Validation is Simpler**
- S3Profile validates: secret exists, has required keys, clusters are valid
- MirrorPeer validates: S3Profile exists, is ready, includes referenced clusters
- No cross-MirrorPeer S3 conflict checking needed

---

## Migration from Current Design

### Current System

The current MirrorPeer has:
- `manageS3 bool` field in MirrorPeerSpec
- S3 management is done directly by MirrorPeer controller
- First MirrorPeer with `manageS3: true` becomes the S3 manager

### Migration Path

Since the current system doesn't have `s3SecretRef` (that was only a proposal), migration is straightforward:

### Phase 1: Introduce S3Profile CR (Additive)

1. **Add S3Profile CRD**
2. **Deploy S3Profile controller**
3. **Keep existing MirrorPeer API** with `manageS3` field (backward compatible)

### Phase 2: Support Both Patterns

MirrorPeer controller supports two modes:
```go
func (r *MirrorPeerReconciler) reconcileS3(ctx context.Context, mp *MirrorPeer) error {
    // Check if we should use new S3Profile pattern
    // Try to find S3Profile for each cluster
    useS3Profile := true
    for _, peerRef := range mp.Spec.Items {
        if _, err := r.getS3ProfileForCluster(ctx, peerRef.ClusterName); err != nil {
            useS3Profile = false
            break
        }
    }
    
    if useS3Profile {
        // New pattern: S3Profile exists for all clusters
        // MirrorPeer doesn't manage S3, just references it
        return nil
    }
    
    // Old pattern: Use manageS3 field
    if mp.Spec.ManageS3 && r.shouldManageS3(ctx, mp) {
        return r.legacyManageS3(ctx, mp)
    }
    
    return nil
}
```

### Phase 3: Migrate Existing MirrorPeers

1. **For each existing MirrorPeer with `manageS3: true`**:
   ```bash
   # 1. Extract S3 configuration from existing setup
   # 2. Create S3Profile CR with the clusters
   # 3. Update MirrorPeer to set manageS3: false
   ```

2. **For MirrorPeers with `manageS3: false`**:
   ```bash
   # 1. Find which MirrorPeer manages their S3
   # 2. Create S3Profile CR (reusing existing S3 config)
   # 3. Update MirrorPeer to set manageS3: false
   ```

### Phase 4: Remove manageS3 (v1beta1)

1. **Remove `manageS3` field** from MirrorPeerSpec
2. **All S3 management via S3Profile** only
3. **Bump API to v1beta1**

---

## Example Scenarios

### Scenario 1: ODF with Internal S3 (Noobaa) - Automated OBC Creation

```yaml
# User creates S3Profile with internalS3 configuration
# CR name "odf-internal-s3" will be used as s3ProfileName in Ramen ConfigMap
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: odf-internal-s3  # This is the s3ProfileName
  namespace: openshift-dr-system
spec:
  internalS3:
    managedCluster: cluster1  # OBC created ONLY on cluster1
    storageClassName: openshift-storage.noobaa.io
    namespace: openshift-storage
    obcName: dr-obc  # Optional, defaults to "odr-odf-internal-s3"
  managedClusters:
  - cluster1  # Uses S3 from local OBC
  - cluster2  # Uses S3 from cluster1's OBC
  - cluster3  # Uses S3 from cluster1's OBC

# S3Profile controller will:
# 1. Create OBC "dr-obc" in namespace "openshift-storage" on cluster1 ONLY (via addon)
# 2. Wait for OBC to be bound and generate secret on cluster1
# 3. Addon transfers secret from cluster1 to hub (creates "odf-internal-s3-s3-secret")
# 4. S3Profile controller copies secret to Ramen operator namespace on hub
# 5. S3Profile controller updates Ramen ConfigMap with S3 profile
#    - S3 endpoint will be from cluster1 (e.g., https://s3.cluster1.example.com)
# 6. S3Profile controller creates DRCluster on hub for each managed cluster
#    - DRCluster for cluster1 (hub-only, not on spoke)
#    - DRCluster for cluster2 (hub-only, not on spoke)
#    - DRCluster for cluster3 (hub-only, not on spoke)
#    - All have spec.s3ProfileName = "odf-internal-s3"
# 
# Result: All three DRClusters (on hub) reference S3 from cluster1's Noobaa/RGW

# Step 2: User creates MirrorPeer
# S3Profile is auto-discovered from cluster names
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: odf-dr
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
    # S3Profile "odf-internal-s3" auto-discovered
  - clusterName: cluster2
    storageClusterRef: {name: ocs-storagecluster}
    # S3Profile "odf-internal-s3" auto-discovered
```

### Scenario 2: Multiple Vendors Sharing External S3

```yaml
# Step 0: Create S3 secret (user-provided for external S3)
apiVersion: v1
kind: Secret
metadata:
  name: aws-s3-secret
  namespace: openshift-dr-system
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "AKIAIOSFODNN7EXAMPLE"
  AWS_SECRET_ACCESS_KEY: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
  s3Bucket: "my-dr-metadata-bucket"
  s3Endpoint: "https://s3.amazonaws.com"
  s3Region: "us-west-2"

---
# Step 1: Create shared S3Profile with external S3
# CR name will be used as s3ProfileName in Ramen ConfigMap
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: shared-aws-s3  # This is the s3ProfileName
spec:
  externalS3:
    secretRef:
      name: aws-s3-secret
      namespace: openshift-dr-system
  managedClusters:
  - cluster1
  - cluster2

# Step 2: Create ODF MirrorPeer
# S3Profile auto-discovered from cluster names
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

# Step 3: Create Dell MirrorPeer (same clusters, different vendor)
# Same S3Profile auto-discovered
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

# Result:
# - Both MirrorPeers auto-discover S3Profile "shared-aws-s3"
# - DRCluster on cluster1 has s3ProfileName = "shared-aws-s3" (shared by both vendors)
# - DRCluster on cluster2 has s3ProfileName = "shared-aws-s3" (shared by both vendors)
```

### Scenario 3: Adding Cluster to Existing S3

```yaml
# Existing S3Profile with cluster1 and cluster2
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: prod-s3  # This is the s3ProfileName
spec:
  externalS3:
    secretRef:
      name: prod-s3-secret
      namespace: openshift-dr-system
  managedClusters:
  - cluster1
  - cluster2

# Day 2: Add cluster3 to same S3
# Just edit the S3Profile:
kubectl patch s3profile prod-s3 --type='json' -p='[
  {"op": "add", "path": "/spec/managedClusters/-", "value": "cluster3"}
]'

# S3Profile controller will:
# 1. Propagate S3 secret to cluster3
# 2. Configure DRCluster on cluster3 with s3ProfileName = "prod-s3"
# 3. Add cluster3 to status.configuredClusters

# Now create MirrorPeer that includes cluster3:
# S3Profile auto-discovered
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: MirrorPeer
metadata:
  name: new-pair
spec:
  vendor: odf
  items:
  - clusterName: cluster1
    storageClusterRef: {name: ocs-storagecluster}
    # S3Profile "prod-s3" auto-discovered
  - clusterName: cluster3
    storageClusterRef: {name: ocs-storagecluster}
    # S3Profile "prod-s3" auto-discovered
```

### Scenario 4: ODF Internal S3 with Custom OBC Name (Backward Compatibility)

```yaml
# For backward compatibility with existing OBC deployments
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: legacy-odf-s3
spec:
  internalS3:
    managedCluster: cluster1  # OBC on cluster1 only
    storageClassName: openshift-storage.noobaa.io
    namespace: openshift-storage
    obcName: existing-obc-name  # Use existing OBC
  managedClusters:
  - cluster1
  - cluster2

# S3Profile controller will:
# 1. Check if OBC "existing-obc-name" already exists in "openshift-storage" on cluster1
# 2. If exists: Use the existing OBC's generated secret
# 3. If not exists: Create new OBC with the specified name
# 4. Transfer secret to hub and configure Ramen ConfigMap
# 5. Both cluster1 and cluster2 use the S3 from cluster1's OBC
```

### Scenario 5: Mixed Topology - Different Clusters for OBC vs DR Pairing

```yaml
# Advanced: OBC on cluster-s3, but DR pairing between cluster1 and cluster2
apiVersion: multicluster.odf.openshift.io/v1alpha1
kind: S3Profile
metadata:
  name: shared-s3-from-cluster-s3
spec:
  internalS3:
    managedCluster: cluster-s3  # Dedicated S3 cluster
    storageClassName: openshift-storage.noobaa.io
    namespace: openshift-storage
  managedClusters:
  - cluster1  # DR cluster (uses S3 from cluster-s3)
  - cluster2  # DR cluster (uses S3 from cluster-s3)
  - cluster-s3  # S3 cluster (hosts the OBC)

# Use case: Dedicated S3 infrastructure cluster
# cluster-s3 hosts the S3 storage
# cluster1 and cluster2 are the DR-paired clusters that use cluster-s3's S3
```

---

## Validation Rules

### S3Profile Validation (Webhook)

```go
// Validating webhook for S3Profile
func (v *S3ProfileValidator) ValidateCreate(ctx context.Context, obj runtime.Object) error {
    s3Profile := obj.(*multiclusterv1alpha1.S3Profile)
    
    // 1. Validate S3 secret exists
    secret := &corev1.Secret{}
    secretKey := types.NamespacedName{
        Name:      s3Profile.Spec.S3SecretRef.Name,
        Namespace: s3Profile.Spec.S3SecretRef.Namespace,
    }
    if err := v.Client.Get(ctx, secretKey, secret); err != nil {
        return fmt.Errorf("S3 secret not found: %s", err)
    }
    
    // 2. Validate secret has required keys
    requiredKeys := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", 
                             "s3Bucket", "s3Endpoint"}
    for _, key := range requiredKeys {
        if _, ok := secret.Data[key]; !ok {
            return fmt.Errorf("S3 secret missing required key: %s", key)
        }
    }
    
    // 3. Validate managedClusters exist
    for _, clusterName := range s3Profile.Spec.ManagedClusters {
        managedCluster := &clusterv1.ManagedCluster{}
        if err := v.Client.Get(ctx, types.NamespacedName{Name: clusterName}, managedCluster); err != nil {
            return fmt.Errorf("ManagedCluster %s not found", clusterName)
        }
    }
    
    // 4. Validate cluster uniqueness across S3Profiles
    // Each cluster can only be in ONE S3Profile's managedClusters list
    s3ProfileList := &multiclusterv1alpha1.S3ProfileList{}
    if err := v.Client.List(ctx, s3ProfileList); err != nil {
        return err
    }
    
    for _, existing := range s3ProfileList.Items {
        if existing.Name == s3Profile.Name {
            continue // Skip self
        }
        
        // Check for cluster overlap
        for _, newCluster := range s3Profile.Spec.ManagedClusters {
            for _, existingCluster := range existing.Spec.ManagedClusters {
                if newCluster == existingCluster {
                    return fmt.Errorf(
                        "cluster %s is already in S3Profile %s managedClusters list. "+
                        "Each cluster can only be in one S3Profile",
                        newCluster, existing.Name)
                }
            }
        }
    }
    
    // 5. Validate internalS3.managedCluster is in spec.managedClusters
    if s3Profile.Spec.InternalS3 != nil {
        obcCluster := s3Profile.Spec.InternalS3.ManagedCluster
        found := false
        for _, cluster := range s3Profile.Spec.ManagedClusters {
            if cluster == obcCluster {
                found = true
                break
            }
        }
        if !found {
            return fmt.Errorf(
                "internalS3.managedCluster %s must be one of the clusters in spec.managedClusters",
                obcCluster)
        }
    }
    
    return nil
}

func (v *S3ProfileValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
    newS3Profile := newObj.(*multiclusterv1alpha1.S3Profile)
    
    // Validate new clusters added to managedClusters exist
    for _, clusterName := range newS3Profile.Spec.ManagedClusters {
        managedCluster := &clusterv1.ManagedCluster{}
        if err := v.Client.Get(ctx, types.NamespacedName{Name: clusterName}, managedCluster); err != nil {
            return fmt.Errorf("ManagedCluster %s not found", clusterName)
        }
    }
    
    // Validate cluster uniqueness (newly added clusters)
    s3ProfileList := &multiclusterv1alpha1.S3ProfileList{}
    if err := v.Client.List(ctx, s3ProfileList); err != nil {
        return err
    }
    
    for _, existing := range s3ProfileList.Items {
        if existing.Name == newS3Profile.Name {
            continue // Skip self
        }
        
        // Check for cluster overlap
        for _, newCluster := range newS3Profile.Spec.ManagedClusters {
            for _, existingCluster := range existing.Spec.ManagedClusters {
                if newCluster == existingCluster {
                    return fmt.Errorf(
                        "cluster %s is already in S3Profile %s managedClusters list",
                        newCluster, existing.Name)
                }
            }
        }
    }
    
    return nil
}

func (v *S3ProfileValidator) ValidateDelete(ctx context.Context, obj runtime.Object) error {
    s3Profile := obj.(*multiclusterv1alpha1.S3Profile)
    
    // Check if any MirrorPeer uses clusters from this S3Profile
    // Since MirrorPeer doesn't have s3ProfileName field, we check by cluster names
    mpList := &multiclusterv1alpha1.MirrorPeerList{}
    if err := v.Client.List(ctx, mpList); err != nil {
        return err
    }
    
    var referencingMirrorPeers []string
    for _, mp := range mpList.Items {
        for _, peerRef := range mp.Spec.Items {
            // Check if this cluster is in the S3Profile being deleted
            for _, managedCluster := range s3Profile.Spec.ManagedClusters {
                if peerRef.ClusterName == managedCluster {
                    referencingMirrorPeers = append(referencingMirrorPeers, mp.Name)
                    break
                }
            }
        }
    }
    
    if len(referencingMirrorPeers) > 0 {
        return fmt.Errorf(
            "cannot delete S3Profile %s: its clusters are used by MirrorPeers: %v. "+
            "Delete the MirrorPeers first",
            s3Profile.Name, referencingMirrorPeers)
    }
    
    return nil
}
```

### MirrorPeer Validation (Updated)

```go
// Updated webhook for MirrorPeer
func (v *MirrorPeerValidator) ValidateCreate(ctx context.Context, obj runtime.Object) error {
    mp := obj.(*multiclusterv1alpha1.MirrorPeer)
    
    // Existing validations...
    
    // NEW: Validate each cluster has an S3Profile configured
    for _, peerRef := range mp.Spec.Items {
        // Find S3Profile that contains this cluster
        s3Profile, err := v.findS3ProfileForCluster(ctx, peerRef.ClusterName)
        if err != nil {
            return fmt.Errorf(
                "no S3Profile found for cluster %s. "+
                "Create an S3Profile with this cluster in managedClusters list first",
                peerRef.ClusterName)
        }
        
        // Optionally check if S3Profile is ready
        if s3Profile.Status.Phase != S3ProfilePhaseReady {
            return fmt.Errorf(
                "S3Profile %s for cluster %s is not ready (current phase: %s). "+
                "Wait for S3Profile to be ready before creating MirrorPeer",
                s3Profile.Name, peerRef.ClusterName, s3Profile.Status.Phase)
        }
    }
    
    return nil
}

func (v *MirrorPeerValidator) findS3ProfileForCluster(
    ctx context.Context, 
    clusterName string,
) (*multiclusterv1alpha1.S3Profile, error) {
    s3ProfileList := &multiclusterv1alpha1.S3ProfileList{}
    if err := v.Client.List(ctx, s3ProfileList); err != nil {
        return nil, err
    }
    
    for i := range s3ProfileList.Items {
        s3Profile := &s3ProfileList.Items[i]
        for _, managedCluster := range s3Profile.Spec.ManagedClusters {
            if managedCluster == clusterName {
                return s3Profile, nil
            }
        }
    }
    
    return nil, fmt.Errorf("no S3Profile found")
}
```

---

## Summary

### Changes Required

1. **New CR**: Add `S3Profile` CRD (CR name is the s3ProfileName)
2. **New Controller**: S3Profile controller
3. **Update MirrorPeer API**: (Eventually) Remove `manageS3` from MirrorPeerSpec
4. **Update MirrorPeer Controller**: Remove S3 management logic, add S3Profile auto-discovery
5. **Update Webhooks**: Add S3Profile webhook (validate cluster uniqueness), update MirrorPeer webhook
6. **Phase 1**: Support both `manageS3` (legacy) and S3Profile (new) patterns
7. **Phase 2**: Deprecate `manageS3` field
8. **Phase 3**: Remove `manageS3` field (v1beta1)

### Key Principles

- **One S3Profile → Many DRClusters** (one-to-many)
- **One DRCluster → One S3Profile** (DRCluster.spec.s3ProfileName is a string, not a list)
- **One Cluster → One S3Profile** (enforced by webhook - cluster can only be in one S3Profile's managedClusters)
- **S3Profile CR name IS the s3ProfileName** (no separate field needed)
- **S3Profile auto-discovered** (MirrorPeer controller looks up S3Profile by cluster name)
- **Internal S3 (ODF)**: OBC created on ONE cluster (internalS3.managedCluster), shared by all clusters
- **External S3**: All clusters use same external S3 endpoint
- **S3Profile manages S3 configuration** (OBC creation, Ramen ConfigMap, secret propagation)
- **MirrorPeer manages peering only** (storage-level peering, DRCluster ownership)
- **S3 lifecycle is independent** of MirrorPeer lifecycle
- **No DRCluster labels needed** (S3Profile discovered by checking managedClusters lists)

### Benefits

✅ **Separation of concerns** - S3 vs DR pairing are independent  
✅ **Reusability** - one S3Profile serves multiple vendors/clusters  
✅ **Flexible topology** - OBC on one cluster, used by many  
✅ **Simplified lifecycle** - manage S3 credentials independently  
✅ **Clearer ownership** - S3Profile owns S3, MirrorPeer owns DRCluster  
✅ **Better Day 2 operations** - credential rotation, adding clusters  
✅ **Auto-discovery** - MirrorPeer has no S3 fields, cleaner API  
✅ **No labels needed** - S3Profile lookup by cluster name  
✅ **Easier to understand** - each CR has single responsibility

---

**Recommendation**: Proceed with this design to achieve clean separation between S3 management and DR pairing.
