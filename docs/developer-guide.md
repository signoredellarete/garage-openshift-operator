# Guida per sviluppatori e maintainer

Questa guida spiega tutto quello che succede "dietro le quinte": come compilare il codice,
come costruire e pubblicare l'immagine Docker su quay.io, come creare release su GitHub,
e come permettere a chiunque di fare il deploy dell'operator da un bastion host
**senza strumenti di compilazione locali**.

---

## Indice

1. [Il flusso completo](#il-flusso-completo)
2. [Setup una-tantum: quay.io](#setup-una-tantum-quayio)
3. [Setup una-tantum: GitHub Secrets](#setup-una-tantum-github-secrets)
4. [Come fare una release](#come-fare-una-release)
5. [Cosa succede automaticamente](#cosa-succede-automaticamente)
6. [Deploy da un bastion host (senza compilazione)](#deploy-da-un-bastion-host-senza-compilazione)
7. [Sviluppo locale (se hai Go e Docker)](#sviluppo-locale-se-hai-go-e-docker)
8. [Struttura dei workflow CI/CD](#struttura-dei-workflow-cicd)
9. [Troubleshooting](#troubleshooting)

---

## Il flusso completo

```
Tu (locale)                  GitHub                        quay.io
──────────                   ──────                        ───────
git tag v0.1.1
git push origin v0.1.1  ──► Trigger "Release" workflow ──► build immagine
                             │                              push tag v0.1.1
                             │                              push latest
                             │
                             ▼
                         GitHub Release creata
                         - install-v0.1.1.yaml allegato
                         - note di rilascio auto-generate

Utente finale (bastion host)
────────────────────────────
kubectl apply -f https://github.com/.../releases/download/v0.1.1/install-v0.1.1.yaml
```

Hai bisogno di due cose per farlo funzionare:
1. Un account **quay.io** con un repository pubblico e un robot account
2. Due **GitHub Secrets** (`QUAY_USERNAME` e `QUAY_TOKEN`) nel repository

---

## Setup una-tantum: quay.io

### 1. Crea un account quay.io

Vai su [https://quay.io](https://quay.io) e registrati (è gratuito per repository pubblici).
Il tuo account quay.io è `signoredellarete`.

### 2. Crea il repository dell'immagine

1. Clicca **"+ Create New Repository"**
2. Repository name: `garage-openshift-operator`
3. Visibilità: **Public** (così chiunque può fare il pull senza autenticarsi)
4. Clicca **"Create Public Repository"**

L'immagine sarà disponibile come:
```
quay.io/signoredellarete/garage-openshift-operator
```

### 3. Crea un Robot Account per il push automatico

Il robot account è un'identità separata usata da GitHub Actions per fare il push.
Non usare il tuo utente personale per questo.

1. Vai su **Account Settings** (icona in alto a destra → "Account Settings")
2. Sezione **"Robot Accounts"** → **"Create Robot Account"**
3. Nome: `github_ci` (il nome completo sarà `signoredellarete+github_ci`)
4. Clicca **"Create"**
5. Nella schermata successiva, assegna i permessi:
   - Repository: `garage-openshift-operator`
   - Permission: **Write**
6. Clicca **"Add permissions"**
7. Torna alla lista dei robot account e clicca su `signoredellarete+github_ci`
8. Nella tab **"Docker Configuration"** o **"Robot Account Token"**:
   - copia il **token** (una stringa lunga, mostrata una volta sola — salvala subito)

Dati che ti servono per il passo successivo:
- **Username**: `signoredellarete+github_ci`
- **Token**: `<stringa lunga copiata>`

---

## Setup una-tantum: GitHub Secrets

I secret permettono a GitHub Actions di autenticarsi su quay.io senza esporre le credenziali nel codice.

1. Vai sul tuo repository GitHub:
   `https://github.com/signoredellarete/garage-openshift-operator`
2. Clicca **Settings** → **Secrets and variables** → **Actions**
3. Clicca **"New repository secret"** e crea questi due secret:

| Nome secret | Valore |
|---|---|
| `QUAY_USERNAME` | `signoredellarete+github_ci` |
| `QUAY_TOKEN` | il token del robot account copiato prima |

Fatto. Non devi fare altro — GitHub Actions li userà in automatico.

---

## Come fare una release

Ogni release corrisponde a un **tag Git semantico** (formato `vMAGGIORE.MINORE.PATCH`).

### Passo 1: prepara il codice

Assicurati che il branch `main` sia aggiornato e funzionante:

```bash
git status          # nessun file modificato
git log --oneline   # il commit che vuoi rilasciare è in cima
```

### Passo 2: crea e pusha il tag

```bash
# Crea il tag localmente (il messaggio apparirà nelle release notes)
git tag v0.1.1 -m "Fix layout initialisation on single-node clusters"

# Pusha il tag su GitHub (questo scatena il workflow di release)
git push origin v0.1.1
```

### Passo 3: aspetta (~3-5 minuti)

Vai su `https://github.com/signoredellarete/garage-openshift-operator/actions`
e guarda il workflow **"Release"** in esecuzione.

Quando finisce, trovi la release su:
`https://github.com/signoredellarete/garage-openshift-operator/releases`

### Passo 4: (opzionale) aggiungi note alla release

GitHub auto-genera le release notes dai commit. Puoi editare manualmente
la descrizione dalla pagina della release su GitHub.

---

## Cosa succede automaticamente

Quando puши il tag `v0.1.1`, il workflow `.github/workflows/release.yml` fa:

```
1. Checkout del codice al tag v0.1.1
2. go build ./...           → verifica che compili
3. docker buildx build      → compila immagine per linux/amd64 E linux/arm64
4. docker push quay.io/signoredellarete/garage-openshift-operator:v0.1.1
5. docker push quay.io/signoredellarete/garage-openshift-operator:0.1   (major.minor)
6. docker push quay.io/signoredellarete/garage-openshift-operator:latest
7. Genera install-v0.1.1.yaml (tutti i manifest concatenati con immagine aggiornata)
8. Crea GitHub Release con il file allegato
```

Il file `install-v0.1.1.yaml` contiene in ordine:
- CRD `GarageCluster`
- CRD `GarageWebUI`
- `ServiceAccount` dell'operator
- `ClusterRole` dell'operator
- `ClusterRoleBinding`
- `Deployment` dell'operator (con immagine `quay.io/signoredellarete/garage-openshift-operator:v0.1.1`)

---

## Deploy da un bastion host (senza compilazione)

Questo è il metodo consigliato per chi non ha Go o Docker installati.
Serve solo `kubectl` (o `oc`) configurato con accesso al cluster.

### Installazione completa in 2 comandi

```bash
# 1. Installa CRD, RBAC e operator (tutto in un file)
kubectl apply -f https://github.com/signoredellarete/garage-openshift-operator/releases/download/v0.1.0/install-v0.1.0.yaml

# 2. Verifica che l'operator sia Running
kubectl -n garage-operator-system get pods
```

Output atteso:
```
NAME                               READY   STATUS    RESTARTS   AGE
garage-operator-5d4f9c8b6-xrpq2    1/1     Running   0          45s
```

### Aggiornamento a una versione più recente

```bash
kubectl apply -f https://github.com/signoredellarete/garage-openshift-operator/releases/download/v0.1.1/install-v0.1.1.yaml
```

Il deploy dell'operator è un `Deployment` con `replicas: 1`, quindi `kubectl apply`
fa un rolling update automatico del pod dell'operator.

### Deploy offline (bastion senza internet)

Se il bastion non ha accesso a internet:

```bash
# Su una macchina con accesso: scarica il file
curl -LO https://github.com/signoredellarete/garage-openshift-operator/releases/download/v0.1.0/install-v0.1.0.yaml

# Copia sul bastion (scp, usb, ecc.) e applica
kubectl apply -f install-v0.1.0.yaml
```

Per i nodi del cluster OpenShift, l'immagine `quay.io/signoredellarete/garage-openshift-operator`
deve essere raggiungibile. Se il cluster è air-gapped, vedi la sezione successiva.

### Deploy in cluster air-gapped

Se il cluster non ha accesso a quay.io, devi copiare l'immagine nel registry interno:

```bash
# Su macchina con accesso a quay.io e al registry interno
podman pull quay.io/signoredellarete/garage-openshift-operator:v0.1.0
podman tag  quay.io/signoredellarete/garage-openshift-operator:v0.1.0 \
            registry.interno.example.com/infra/garage-openshift-operator:v0.1.0
podman push registry.interno.example.com/infra/garage-openshift-operator:v0.1.0

# Modifica install.yaml per usare il registry interno
sed 's|quay.io/signoredellarete|registry.interno.example.com/infra|g' \
    install-v0.1.0.yaml | kubectl apply -f -
```

---

## Sviluppo locale (se hai Go e Docker)

### Prerequisiti

```bash
# Installa Go 1.22+
# Linux:
wget https://go.dev/dl/go1.22.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.22.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# Verifica
go version    # go version go1.22.x linux/amd64
```

### Build del binario

```bash
cd garage-openshift-operator
go mod download    # scarica le dipendenze (~400 MB la prima volta)
go build ./...     # compila tutto, nessun output se ok
go vet ./...       # analisi statica
```

### Esecuzione locale (senza Docker)

Utile per sviluppare: il controller gira sulla tua macchina e usa il kubeconfig locale
per parlare con il cluster OpenShift.

```bash
# Installa prima i CRD nel cluster
kubectl apply -f config/crd/bases/

# Avvia il controller in foreground (Ctrl+C per fermare)
go run main.go
```

### Build dell'immagine Docker

```bash
# Build per la tua architettura locale
docker build -t quay.io/signoredellarete/garage-openshift-operator:dev .

# Push (devi essere autenticato: docker login quay.io)
docker push quay.io/signoredellarete/garage-openshift-operator:dev

# Per testare subito nel cluster:
kubectl set image deployment/garage-operator \
  manager=quay.io/signoredellarete/garage-openshift-operator:dev \
  -n garage-operator-system
```

### Build multi-architettura (come fa la CI)

```bash
# Richiede Docker Buildx
docker buildx create --use
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --push \
  -t quay.io/signoredellarete/garage-openshift-operator:dev \
  .
```

### Rigenerare CRD e DeepCopy (dopo aver modificato i tipi Go)

```bash
# Installa controller-gen
go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

# Rigenera
controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./..."
controller-gen crd paths="./..." output:crd:artifacts:config=config/crd/bases
```

---

## Struttura dei workflow CI/CD

```
.github/workflows/
├── ci.yml       → eseguito su ogni push a main e su ogni PR
│                  - go build
│                  - go vet
│                  - go test
│
└── release.yml  → eseguito solo quando si pusha un tag vX.Y.Z
                   - go build (verifica)
                   - docker buildx build + push su quay.io
                   - genera install-vX.Y.Z.yaml
                   - crea GitHub Release con file allegato
```

### Aggiungere un altro registry (es. GHCR)

GitHub Container Registry (ghcr.io) non richiede un account separato: usa il
`GITHUB_TOKEN` automatico. Per abilitarlo nel workflow:

```yaml
# In release.yml, aggiungi dopo il login a quay.io:
- name: Log in to GHCR
  uses: docker/login-action@v3
  with:
    registry: ghcr.io
    username: ${{ github.actor }}
    password: ${{ secrets.GITHUB_TOKEN }}

# E aggiungi l'immagine GHCR in metadata-action:
images: |
  quay.io/${{ github.repository_owner }}/garage-openshift-operator
  ghcr.io/${{ github.repository }}
```

---

## Troubleshooting

### Il workflow "Release" fallisce al push su quay.io

```
Error: unauthorized: access to the requested resource is not authorized
```

Cause possibili:
1. I secret `QUAY_USERNAME` / `QUAY_TOKEN` non sono configurati → vai a **Settings → Secrets**
2. Il robot account non ha permesso di write sul repository → vai su quay.io e verifica
3. Il repository è scritto male nel workflow → deve essere `quay.io/signoredellarete/garage-openshift-operator`

### Il workflow "CI" fallisce su `go build`

```
cannot find module providing package ...
```

Il `go.mod` è disallineato. Esegui localmente:
```bash
go mod tidy
git add go.mod go.sum
git commit -m "chore: tidy go modules"
git push
```

### L'operator parte ma non riconcilia nulla

Verifica i log:
```bash
kubectl -n garage-operator-system logs deploy/garage-operator -f
```

Se vedi `no kind is registered for the type`, il CRD non è installato:
```bash
kubectl apply -f config/crd/bases/
```

### Come verificare che l'immagine sia stata pubblicata correttamente

```bash
# Da qualsiasi macchina con podman/docker:
podman pull quay.io/signoredellarete/garage-openshift-operator:v0.1.0
podman inspect quay.io/signoredellarete/garage-openshift-operator:v0.1.0 | grep Architecture
```

Oppure naviga su: `https://quay.io/repository/signoredellarete/garage-openshift-operator?tab=tags`
