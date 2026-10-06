# S3Profile Internal S3 Flow - Visual Diagram

## Overview

This diagram shows how S3Profile with `internalS3` creates OBC on managed clusters via the addon mechanism.

---

## Flow Diagram: ODF Internal S3 (OBC Creation)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              HUB CLUSTER                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. User creates S3Profile                                                   │
│  ┌──────────────────────────────────────────┐                              │
│  │ apiVersion: v1alpha1                     │                              │
│  │ kind: S3Profile                          │                              │
│  │ metadata:                                │                              │
│  │   name: odf-internal-s3                  │                              │
│  │ spec:                                    │                              │
│  │   internalS3:                            │                              │
│  │     managedCluster: cluster1             │  ◄─ OBC created HERE only   │
│  │     storageClassName: noobaa.io          │                              │
│  │     namespace: openshift-storage         │                              │
│  │     obcName: dr-obc                      │                              │
│  │   managedClusters:                       │                              │
│  │   - cluster1  (local S3)                 │  ◄─ Uses S3 from cluster1   │
│  │   - cluster2  (remote S3)                │  ◄─ Uses S3 from cluster1   │
│  └──────────────────────────────────────────┘                              │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │              S3Profile Controller                                    │   │
│  │  2. Reconciles S3Profile CR                                          │   │
│  │  3. Detects internalS3 configuration                                 │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  4. Creates ManifestWork ONLY for cluster1 (internalS3.managedCluster)│   │
│  │     with OBC manifest + callback annotation                          │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│            │                                                                 │
│            │                                                                 │
└────────────┼─────────────────────────────────────────────────────────────────┘
             │
             │
    ┌────────▼────────┐
    │  ManifestWork   │
    │  for cluster1   │  ◄─ OBC created ONLY on cluster1
    └────────┬────────┘
             │
             ▼
┌────────────────────────────┐     ┌────────────────────────────┐
│     SPOKE: cluster1        │     │     SPOKE: cluster2        │
├────────────────────────────┤     ├────────────────────────────┤
│  (OBC CREATED HERE)        │     │  (NO OBC - uses cluster1)  │
│                            │     │                            │
│  5. Addon watches          │     │                            │
│     ManifestWork           │     │                            │
│                            │     │                            │
│  6. Creates OBC            │     │                            │
│  ┌──────────────────────┐ │     │                            │
│  │ kind: OBC            │ │     │                            │
│  │ name: dr-obc         │ │     │                            │
│  │ namespace:           │ │     │                            │
│  │   openshift-storage  │ │     │                            │
│  └──────────────────────┘ │     │                            │
│            │               │     │                            │
│            ▼               │     │                            │
│  ┌──────────────────────┐ │     │                            │
│  │ Noobaa/RGW           │ │     │                            │
│  │ (ODF Operator)       │ │     │                            │
│  └──────────────────────┘ │     │                            │
│            │               │     │                            │
│  7. Binds OBC             │     │                            │
│  8. Generates Secret      │     │                            │
│            │               │     │                            │
│            ▼               │     │                            │
│  ┌──────────────────────┐ │     │                            │
│  │ Secret (OBC-generated│ │     │                            │
│  │ on spoke)            │ │     │                            │
│  │ - AWS_ACCESS_KEY_ID  │ │     │                            │
│  │ - AWS_SECRET_ACCESS_ │ │     │                            │
│  │   KEY                │ │     │                            │
│  │ - s3Bucket           │ │     │                            │
│  │ - s3Endpoint:        │ │     │                            │
│  │   cluster1 S3 URL    │ ◄────┼─ Shared by cluster2         │
│  └──────────────────────┘ │     │                            │
│            │               │     │                            │
│            ▼               │     │                            │
│  ┌──────────────────────┐ │     │                            │
│  │ Addon detects OBC    │ │     │                            │
│  │ bound + secret ready │ │     │                            │
│  └──────────────────────┘ │     │                            │
│            │               │     │                            │
│            │               │     │                            │
│  9. Transfer secret to hub │     │                            │
│     via callback           │     │                            │
└────────────┬───────────────┘     └────────────────────────────┘
             │
             │
                          │
                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              HUB CLUSTER                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  10. Addon creates Secret on hub                                            │
│  ┌──────────────────────────────────────────────────────────────────┐      │
│  │ apiVersion: v1                                                   │      │
│  │ kind: Secret                                                     │      │
│  │ metadata:                                                        │      │
│  │   name: odf-internal-s3-s3-secret  # <s3profile-name>-s3-secret │      │
│  │   namespace: openshift-dr-system                                │      │
│  │ data:                                                            │      │
│  │   AWS_ACCESS_KEY_ID: ...                                         │      │
│  │   AWS_SECRET_ACCESS_KEY: ...                                     │      │
│  │   s3Bucket: dr-obc-<hash>                                        │      │
│  │   s3Endpoint: https://s3.cluster1.example.com (from cluster1)   │      │
│  │   s3Region: us-east-1                                            │      │
│  └──────────────────────────────────────────────────────────────────┘      │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │           S3Profile Controller detects secret exists               │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  11. Updates Ramen ConfigMap with S3 profile                       │   │
│  │  ┌───────────────────────────────────────────────────────────┐     │   │
│  │  │ ramenConfig:                                              │     │   │
│  │  │   s3StoreProfiles:                                        │     │   │
│  │  │   - s3ProfileName: odf-internal-s3                        │     │   │
│  │  │     s3Bucket: dr-obc-<hash>                               │     │   │
│  │  │     s3CompatibleEndpoint: https://s3.cluster1...          │     │   │
│  │  │     s3Region: us-east-1                                   │     │   │
│  │  └───────────────────────────────────────────────────────────┘     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  12. Copies S3 secret to Ramen operator namespace on hub           │   │
│  │      (e.g., openshift-dr-system)                                   │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  13. Creates/Updates DRCluster on HUB for each managed cluster     │   │
│  │  ┌────────────────────────────────────────────────────────┐         │   │
│  │  │ DRCluster (on hub, name: cluster1)                    │         │   │
│  │  │ spec:                                                  │         │   │
│  │  │   s3ProfileName: odf-internal-s3                       │         │   │
│  │  └────────────────────────────────────────────────────────┘         │   │
│  │  ┌────────────────────────────────────────────────────────┐         │   │
│  │  │ DRCluster (on hub, name: cluster2)                    │         │   │
│  │  │ spec:                                                  │         │   │
│  │  │   s3ProfileName: odf-internal-s3                       │         │   │
│  │  └────────────────────────────────────────────────────────┘         │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  NOTE: DRCluster is a hub-only API                                          │
│        - NOT deployed to spoke clusters                                     │
│        - Created on hub, represents spoke cluster configuration             │
│        - Ramen operator on hub reads DRCluster + S3 secret to manage DR     │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘

SPOKE CLUSTERS (cluster1, cluster2):
  - Only have OBC (on cluster1) for internal S3
  - No DRCluster deployed on spokes
  - No S3 secrets deployed on spokes
  - DR is managed entirely from hub via Ramen operator
```

---

## Flow Diagram: External S3

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              HUB CLUSTER                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. User creates S3 Secret (manually or via UI)                             │
│  ┌──────────────────────────────────────────────────────────────────┐      │
│  │ apiVersion: v1                                                   │      │
│  │ kind: Secret                                                     │      │
│  │ metadata:                                                        │      │
│  │   name: aws-s3-secret                                            │      │
│  │   namespace: openshift-dr-system                                │      │
│  │ stringData:                                                      │      │
│  │   AWS_ACCESS_KEY_ID: "AKIAIOSFODNN7EXAMPLE"                      │      │
│  │   AWS_SECRET_ACCESS_KEY: "wJalrXUtn..."                          │      │
│  │   s3Bucket: "my-dr-metadata-bucket"                              │      │
│  │   s3Endpoint: "https://s3.amazonaws.com"                         │      │
│  │   s3Region: "us-west-2"                                          │      │
│  └──────────────────────────────────────────────────────────────────┘      │
│                                                                              │
│  2. User creates S3Profile referencing the secret                           │
│  ┌──────────────────────────────────────────┐                              │
│  │ apiVersion: v1alpha1                     │                              │
│  │ kind: S3Profile                          │                              │
│  │ metadata:                                │                              │
│  │   name: shared-aws-s3                    │                              │
│  │ spec:                                    │                              │
│  │   externalS3:                            │                              │
│  │     secretRef:                           │                              │
│  │       name: aws-s3-secret                │                              │
│  │       namespace: openshift-dr-system     │                              │
│  │   managedClusters:                       │                              │
│  │   - cluster1                             │                              │
│  │   - cluster2                             │                              │
│  └──────────────────────────────────────────┘                              │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │              S3Profile Controller                                    │   │
│  │  3. Reconciles S3Profile CR                                          │   │
│  │  4. Detects externalS3 configuration                                 │   │
│  │  5. Reads secret "aws-s3-secret"                                     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  6. Updates Ramen ConfigMap with S3 profile                          │   │
│  │  ┌───────────────────────────────────────────────────────────┐       │   │
│  │  │ ramenConfig:                                              │       │   │
│  │  │   s3StoreProfiles:                                        │       │   │
│  │  │   - s3ProfileName: shared-aws-s3                          │       │   │
│  │  │     s3Bucket: my-dr-metadata-bucket                       │       │   │
│  │  │     s3CompatibleEndpoint: https://s3.amazonaws.com        │       │   │
│  │  │     s3Region: us-west-2                                   │       │   │
│  │  └───────────────────────────────────────────────────────────┘       │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  7. Copies S3 secret to Ramen operator namespace on hub            │   │
│  │     (e.g., openshift-dr-system)                                    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                      │                                                       │
│                      ▼                                                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  8. Creates/Updates DRCluster on HUB for each managed cluster      │   │
│  │  ┌────────────────────────────────────────────────────────┐         │   │
│  │  │ DRCluster (on hub, name: cluster1)                    │         │   │
│  │  │ spec:                                                  │         │   │
│  │  │   s3ProfileName: shared-aws-s3                         │         │   │
│  │  └────────────────────────────────────────────────────────┘         │   │
│  │  ┌────────────────────────────────────────────────────────┐         │   │
│  │  │ DRCluster (on hub, name: cluster2)                    │         │   │
│  │  │ spec:                                                  │         │   │
│  │  │   s3ProfileName: shared-aws-s3                         │         │   │
│  │  └────────────────────────────────────────────────────────┘         │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  NOTE: DRCluster is a hub-only API - NOT on spoke clusters                  │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘

SPOKE CLUSTERS (cluster1, cluster2):
  - No DRCluster deployed
  - No S3 secrets deployed
  - DR is managed entirely from hub
```

---

## Key Points

### Internal S3 (ODF)
- **S3Profile controller** triggers OBC creation via ManifestWork on **ONE cluster** (internalS3.managedCluster)
- **Addon on that spoke** creates OBC, waits for binding, extracts secret
- **Addon** transfers secret from spoke → hub
- **S3Profile controller** copies secret to Ramen operator namespace on hub
- **S3Profile controller** creates S3 profile in Ramen ConfigMap on hub
- **S3Profile controller** creates DRCluster on **hub** for each managed cluster
- **S3 endpoint** is from the cluster where OBC was created (e.g., cluster1)
- **All DRClusters** (on hub) reference that same S3 endpoint

### External S3
- **User** provides S3 secret on hub
- **S3Profile controller** reads existing secret
- **S3Profile controller** copies secret to Ramen operator namespace on hub
- **S3Profile controller** creates S3 profile in Ramen ConfigMap on hub
- **S3Profile controller** creates DRCluster on **hub** for each managed cluster
- **No OBC creation** needed
- **All DRClusters** (on hub) reference the same external S3

### Key Points
- **DRCluster is hub-only** - NOT deployed to spoke clusters
- **S3 secret is hub-only** - copied to Ramen operator namespace on hub
- **Ramen ConfigMap is on hub** - Ramen operator reads from hub
- **S3Profile CR name** = s3ProfileName in Ramen ConfigMap and DRCluster
- **MirrorPeer** auto-discovers S3Profile by cluster name (no s3ProfileName field needed)
