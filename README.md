# garage-openshift-operator

Un operator Kubernetes/OpenShift per [Garage](https://garagehq.deuxfleurs.fr/) — uno storage
a oggetti distribuito, compatibile con S3 API — e per la sua interfaccia web
[garage-webui](https://github.com/khairul169/garage-webui).

---

## Indice

1. [Prerequisiti](#prerequisiti)
2. [Installazione](#installazione)
3. [GarageCluster — reference](#garagecluster--reference)
4. [GarageWebUI — reference](#garagewebui--reference)
5. [Aggiornamento automatico](#aggiornamento-automatico)
6. [Accesso alle credenziali e S3 API](#accesso-alle-credenziali-e-s3-api)
7. [Esempi](#esempi)
8. [Roadmap](#roadmap)

---

## Prerequisiti

- OpenShift 4.10+ (o Kubernetes 1.25+)
- `kubectl` / `oc` con accesso cluster-admin
- Connettività verso `quay.io` dai nodi del cluster (per il pull dell'immagine operator)

> Per informazioni su come compilare il codice, pubblicare release e gestire il progetto
> come maintainer, consulta [`docs/developer-guide.md`](docs/developer-guide.md).

---

## Installazione

### Metodo consigliato: un singolo `kubectl apply`

Scarica e applica il manifesto all-in-one dalla
[pagina delle release](https://github.com/signoredellarete/garage-openshift-operator/releases):

```bash
kubectl apply -f https://github.com/signoredellarete/garage-openshift-operator/releases/download/v0.1.0/install-v0.1.0.yaml
```

Questo installa in un solo comando:
- Le due CRD (`GarageCluster`, `GarageWebUI`)
- `ServiceAccount`, `ClusterRole`, `ClusterRoleBinding` dell'operator
- Il `Deployment` dell'operator nel namespace `garage-operator-system`

### Verifica

```bash
kubectl -n garage-operator-system get pods
# NAME                               READY   STATUS    RESTARTS   AGE
# garage-operator-5d4f9c8b6-xrpq2    1/1     Running   0          30s
```

### Aggiornamento dell'operator

```bash
kubectl apply -f https://github.com/signoredellarete/garage-openshift-operator/releases/download/v0.1.1/install-v0.1.1.yaml
```

---

## GarageCluster — reference

`GarageCluster` gestisce il ciclo di vita di un cluster Garage S3.

### Spec

| Campo | Tipo | Default | Descrizione |
|---|---|---|---|
| `replicas` | integer | `3` | Numero di pod Garage |
| `version` | string | `v1.0.1` | Tag immagine Garage (es. `"v1.0.1"`) |
| `image` | string | — | Immagine Docker custom (sovrascrive `dxflrs/garage:<version>`) |
| `storage.metaStorageSize` | string | `3Gi` | PVC per i metadata Garage (consigliato SSD) |
| `storage.dataStorageSize` | string | `30Gi` | PVC per i dati oggetto |
| `storage.storageClassName` | string | — | StorageClass per i PVC (usa il default del cluster se omesso) |
| `config.s3Region` | string | `garage` | Region restituita nelle risposte S3 |
| `config.replicationFactor` | integer | `1` | Copie di ogni blocco dati. Deve essere ≤ `replicas` |
| `config.zone` | string | `dc1` | Zona datacenter assegnata ai nodi nel layout |
| `config.dbEngine` | string | `lmdb` | Backend metadata: `lmdb` o `sqlite` |
| `config.consistencyMode` | string | `consistent` | `consistent` / `degraded` / `dangerous` |
| `config.s3RootDomain` | string | — | Root domain per accesso S3 vhost-style (es. `.s3.example.com`) |
| `config.webRootDomain` | string | — | Root domain per static website hosting |
| `config.blockSize` | string | — | Dimensione chunk (es. `"10MiB"` per file grandi) |
| `config.compressionLevel` | integer | — | Livello compressione zstd (-99÷22); `0` = default (1) |
| `expose.s3APIRoute.enabled` | boolean | `false` | Crea una Route OpenShift per la S3 API |
| `expose.s3APIRoute.hostname` | string | — | Hostname della Route (auto-generato da OpenShift se vuoto) |
| `expose.s3APIRoute.tlsTermination` | string | `edge` | `edge` / `passthrough` / `reencrypt` |
| `expose.webRoute.enabled` | boolean | `false` | Route per il website hosting |
| `expose.adminRoute.enabled` | boolean | `false` | Route per l'admin API (usare con cautela) |
| `autoUpdate.enabled` | boolean | `false` | Abilita controllo e aggiornamento automatico della versione |
| `autoUpdate.schedule` | string | `0 2 * * *` | Espressione cron per il controllo versione |
| `autoUpdate.allowPreRelease` | boolean | `false` | Considera anche le pre-release (rc, beta) |
| `rpcSecretRef` | SecretKeyRef | — | Secret esistente per l'RPC secret (generato automaticamente se omesso) |
| `adminTokenRef` | SecretKeyRef | — | Secret esistente per l'admin token (generato automaticamente se omesso) |
| `resources` | ResourceRequirements | — | CPU/memory per i pod Garage |
| `nodeSelector` | map | — | Seleziona i nodi su cui fare il deploy |
| `tolerations` | list | — | Tollerazioni per i pod Garage |

### Status

```yaml
status:
  phase: "Ready"               # Provisioning | Ready | Degraded | Updating
  readyReplicas: 3
  currentVersion: "v1.0.1"
  availableVersion: "v1.0.2"   # popolato quando auto-update rileva una nuova versione
  layoutApplied: true           # true dopo l'init automatico del layout
  s3Endpoint: "http://my-garage-s3.garage.svc.cluster.local:3900"
  adminEndpoint: "http://my-garage-admin.garage.svc.cluster.local:3903"
  conditions:
    - type: Ready
      status: "True"
```

---

## GarageWebUI — reference

`GarageWebUI` deploya l'interfaccia web di amministrazione Garage.

### Spec

| Campo | Tipo | Default | Descrizione |
|---|---|---|---|
| `replicas` | integer | `1` | Numero di pod WebUI |
| `version` | string | `v1.1.0` | Tag immagine garage-webui |
| `garageClusterRef.name` | string | — | Nome del `GarageCluster` nello stesso namespace **(obbligatorio)** |
| `expose.route.enabled` | boolean | `false` | Crea una Route OpenShift |
| `expose.route.hostname` | string | — | Hostname della Route |
| `expose.route.tlsTermination` | string | `edge` | `edge` / `passthrough` / `reencrypt` |
| `auth.secretRef` | SecretKeyRef | — | Secret con credenziali `user:bcrypt_hash` per basic auth |
| `autoUpdate.enabled` | boolean | `false` | Aggiornamento automatico garage-webui |
| `resources` | ResourceRequirements | — | CPU/memory per i pod WebUI |

### Status

```yaml
status:
  phase: "Ready"
  readyReplicas: 1
  currentVersion: "v1.1.0"
  url: "https://garage-ui.example.com"   # URL della Route OpenShift
```

---

## Aggiornamento automatico

Con `autoUpdate.enabled: true` l'operator:

1. Interroga [GitHub Releases API](https://api.github.com/repos/deuxfleurs-org/garage/releases/latest)
   con la frequenza definita da `autoUpdate.schedule`
2. Confronta il tag restituito con `spec.version` usando semver
3. Se esiste una versione più recente:
   - Aggiorna `status.availableVersion`
   - Emette un evento Kubernetes `UpdateAvailable`
   - Aggiorna automaticamente `spec.version` → rolling update dello StatefulSet

```bash
# Monitora gli eventi di aggiornamento
kubectl get events -n garage --field-selector reason=UpdateAvailable
kubectl get events -n garage --field-selector reason=AutoUpdate
```

---

## Accesso alle credenziali e S3 API

L'operator crea automaticamente un Secret `<cluster-name>-secrets` con l'RPC secret
e l'admin token.

```bash
# Admin token (per API calls dirette)
kubectl get secret my-garage-secrets -n garage \
  -o jsonpath='{.data.admin-token}' | base64 -d

# Endpoint S3 in-cluster
kubectl get garagecluster my-garage -n garage \
  -o jsonpath='{.status.s3Endpoint}'
```

### Gestire chiavi S3 tramite admin API

```bash
ADMIN_URL=$(kubectl get garagecluster my-garage -n garage \
  -o jsonpath='{.status.adminEndpoint}')
TOKEN=$(kubectl get secret my-garage-secrets -n garage \
  -o jsonpath='{.data.admin-token}' | base64 -d)

# Crea una chiave S3
curl -s -XPOST -H "Authorization: Bearer $TOKEN" "$ADMIN_URL/v1/key?name=myapp"
```

### Abilitare basic auth su garage-webui

```bash
# Genera hash bcrypt della password
htpasswd -bnBC 12 admin mypassword | tr -d '\n'
# Output: admin:$2y$12$...

# Crea il Secret
kubectl create secret generic garage-webui-auth \
  --from-literal=credentials='admin:$2y$12$...' \
  -n garage

# Referenzialo nella CR
spec:
  auth:
    secretRef:
      name: garage-webui-auth
      key: credentials
```

---

## Esempi

### Cluster single-node per test/sviluppo

```yaml
apiVersion: storage.garage.io/v1alpha1
kind: GarageCluster
metadata:
  name: garage-dev
  namespace: garage
spec:
  replicas: 1
  version: "v1.0.1"
  storage:
    metaStorageSize: "1Gi"
    dataStorageSize: "10Gi"
  config:
    replicationFactor: 1
    zone: "local"
```

### Cluster produzione 3 nodi con auto-update e Route S3

```yaml
apiVersion: storage.garage.io/v1alpha1
kind: GarageCluster
metadata:
  name: garage-prod
  namespace: garage
spec:
  replicas: 3
  version: "v1.0.1"
  autoUpdate:
    enabled: true
    schedule: "0 4 * * 0"   # ogni domenica alle 04:00 UTC
  storage:
    metaStorageSize: "10Gi"
    dataStorageSize: "500Gi"
    storageClassName: "fast-nvme"
  config:
    replicationFactor: 3
    zone: "prod"
    s3Region: "eu-west-1"
    blockSize: "10MiB"
    compressionLevel: 3
  expose:
    s3APIRoute:
      enabled: true
      hostname: "s3.example.com"
      tlsTermination: "edge"
  resources:
    requests:
      cpu: "500m"
      memory: "1Gi"
    limits:
      cpu: "4"
      memory: "8Gi"
  nodeSelector:
    node-role.kubernetes.io/storage: "true"
```

### WebUI collegata al cluster produzione

```yaml
apiVersion: storage.garage.io/v1alpha1
kind: GarageWebUI
metadata:
  name: garage-prod-ui
  namespace: garage
spec:
  replicas: 1
  version: "v1.1.0"
  garageClusterRef:
    name: garage-prod
  autoUpdate:
    enabled: true
  expose:
    route:
      enabled: true
      hostname: "garage-ui.example.com"
      tlsTermination: "edge"
```

---

## Roadmap

### v0.1.0 (corrente)
- [x] CRD `GarageCluster` e `GarageWebUI`
- [x] StatefulSet con PVC per metadata e dati
- [x] Inizializzazione automatica del layout cluster
- [x] Secret auto-generati (rpc-secret, admin-token)
- [x] Route OpenShift opzionali per S3, web, admin
- [x] Auto-update via GitHub Releases API con semver comparison
- [x] Kubernetes peer discovery automatico (`deuxfleurs.fr/garagenodes`)

### v0.2.0
- [ ] Supporto multi-zona nel layout
- [ ] Modalità `notifyOnly`: notifica senza aggiornare automaticamente
- [ ] PVC resize automatico
- [ ] Prometheus `ServiceMonitor`

### v0.3.0
- [ ] Bundle OLM per l'auto-aggiornamento dell'operator stesso
- [ ] Webhook di validazione CRD
- [ ] TLS inter-nodo (mTLS per RPC)
