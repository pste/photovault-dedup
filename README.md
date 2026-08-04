# photovault-dedup

Cron di ricerca duplicati, in Go. Gira come CronJob Kubernetes.

Non cammina l'albero delle cartelle: **prende i file dal database**. Chiede all'API quali
media non hanno ancora un hash, li calcola leggendo dalla share, li rimanda indietro, e infine
chiede all'API di ricostruire i gruppi di duplicati.

La share è montata in **sola lettura**: questo servizio non cancella e non sposta nulla. Le
cancellazioni approvate dall'utente le esegue `photovault-scan` (job `trashapply`), l'unico
componente con il mount in scrittura.

## Cosa fa, in ordine

1. `GET /api/internal/dedup/pending?stage=hash` → media senza `content_hash`
   → legge l'originale dalla share → **sha256** → `POST /api/internal/dedup/hashes`
2. `GET /api/internal/dedup/pending?stage=dhash` → media con thumbnail pronta ma senza `dhash`
   → legge la thumbnail `s` → **dHash a 64 bit** → `POST /api/internal/dedup/hashes`
3. `POST /api/internal/dedup/rebuild` → l'API esegue la SQL di raggruppamento e materializza
   `dup_groups` / `dup_members`

## Requisiti

- La share montata in sola lettura su `MEDIA_ROOT`
- `photovault-api` raggiungibile
- Go non serve installato: build e test in Docker, come in `photovault-scan`

## Variabili d'ambiente

```
API_URL=http://localhost:3000
API_TOKEN=
MEDIA_ROOT=/data/photos
LOG_LEVEL=trace
DEDUP_WORKERS=3
DEDUP_BATCH=200
GOMEMLIMIT=400MiB
```

## Sviluppo

```bash
sh private/go.sh build ./...
sh private/go.sh test ./...

docker build -f .docker/Dockerfile -t photovault-dedup:dev .
docker run --rm --network host \
  -e API_URL=http://localhost:3000 -e API_TOKEN=dev -e MEDIA_ROOT=/data/photos \
  -v ~/Pictures:/data/photos \
  photovault-dedup:dev
```

## Scelte implementative

### Due hash, due scopi

**sha256 — duplicati esatti.** Si legge l'originale dalla share. È il passaggio costoso: una
libreria da 500 GB richiede circa 75 minuti a velocità di rete piena. È il prezzo di avere il
dedup come servizio indipendente invece che dentro lo scan, ed è accettabile perché è un job
notturno e incrementale: solo i media senza hash vengono letti, quindi dopo il primo giro
costa quasi nulla.

Per i video oltre i 256 MB si usa `hash_kind='sha256-partial'`: sha256 di (primi 16 MB ‖
ultimi 16 MB ‖ dimensione del file). Su video da fotocamera è esatto in pratica ed evita 40
secondi di lettura per file.

**dHash — duplicati simili.** Si calcola dalla thumbnail `s`, non dall'originale: sono ~15 KB
invece di 6 MB, ed è già ridimensionata.

dHash e non pHash: ridimensiona a 9×8 in scala di grigi e confronta i pixel orizzontalmente
adiacenti, 64 bit in una dozzina di righe, senza DCT. È robusto proprio dove serve — la stessa
foto riesportata da WhatsApp o Telegram a dimensione e qualità diverse. La robustezza in più
di pHash sulla gamma costa un'implementazione della DCT e produce più falsi positivi a
distanze di Hamming basse.

### Il tipo della colonna è `bit(64)`

Un dHash è un unsigned a 64 bit: in `bigint` diventerebbe negativo metà delle volte, e ogni
query richiederebbe un cast `::bit(64)`. Con `bit(64)` invece `#` è direttamente lo XOR e
`bit_count()` è core PostgreSQL 14 — **nessuna estensione da installare**. Go scrive il valore
con `fmt.Sprintf("%064b", h)`.

### Il confronto è incrementale

Un self-join completo su 50.000 righe sono 1,25 miliardi di valutazioni: minuti di lavoro
ogni notte, per niente. Vengono invece confrontate contro l'intero corpus **solo le righe con
`dedup_checked IS NULL`**: il primo giro è quello caro, i successivi sono 500 righe nuove
contro 50.000, cioè circa un secondo.

Questo batte l'LSH banding sia in semplicità sia in correttezza: il banding con 4 bande da 16
bit garantisce il recall completo solo fino a distanza 3, per arrivare a 8 ne servirebbero 9.

Guard `bit_count(dhash) BETWEEN 8 AND 56`: scarta le immagini piatte, nere o bianche, il cui
dHash è degenere e finirebbe per somigliare a tutto, creando un unico enorme gruppo di falsi
positivi.

### Il raggruppamento sta nell'API

La chiusura transitiva delle coppie simili si fa con una union-find in JavaScript dentro
l'API, una ventina di righe: molto più leggibile e più facile da debuggare di una CTE
ricorsiva. Il risultato viene materializzato in `dup_groups` / `dup_members`, così la UI legge
una tabella e non esegue mai il join.

I duplicati esatti non hanno bisogno di alcun join:

```sql
SELECT content_hash, count(*) AS n, sum(file_size) - min(file_size) AS bytes_wasted
FROM media
WHERE content_hash IS NOT NULL AND missing_since IS NULL
GROUP BY content_hash
HAVING count(*) > 1;
```

### Chi tenere

Il keeper viene proposto automaticamente e confermato dall'utente: più pixel → file più
grande → mtime più vecchio → percorso più corto.
