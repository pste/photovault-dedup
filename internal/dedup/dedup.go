// Package dedup svuota le due code di hash e chiede all'API di ricostruire i
// gruppi di duplicati.
package dedup

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pste/photovault-dedup/internal/api"
	"github.com/pste/photovault-dedup/internal/hash"
)

const privateDir = ".photovault"

type Config struct {
	MediaRoot string
	Batch     int
}

type Dedup struct {
	cfg    Config
	client *api.Client
	log    *slog.Logger
}

func New(cfg Config, client *api.Client, log *slog.Logger) *Dedup {
	return &Dedup{cfg: cfg, client: client, log: log}
}

// Le thumbnail sono distribuite su 256 sottocartelle in base al media_id:
// stessa formula usata da photovault-scan quando le genera.
func (d *Dedup) thumbPath(mediaID int, size string) string {
	shard := fmt.Sprintf("%02x", mediaID%256)
	name := strconv.Itoa(mediaID) + "_" + size + ".jpg"
	return filepath.Join(d.cfg.MediaRoot, privateDir, "thumbs", shard, name)
}

func (d *Dedup) originalPath(item api.PendingMedia) string {
	return filepath.Join(d.cfg.MediaRoot, item.RelPath, item.FolderPath, item.FileName)
}

// Run esegue le tre fasi in ordine: sha256, dHash, raggruppamento.
func (d *Dedup) Run(jobID int) (string, error) {
	hashed, err := d.hashOriginals(jobID)
	if err != nil {
		return "", err
	}
	return d.perceptualAndGroups(jobID, hashed)
}

// RunPerceptual salta gli sha256 e fa solo la parte percettiva.
//
// Serve perche' le due meta' hanno costi incomparabili: gli sha256 leggono ogni
// originale -- 830 GB sull'archivio vero -- mentre i dHash leggono le thumbnail
// 's', 15 KB l'una. Chi vuole vedere i duplicati **simili** senza aspettare un
// giorno di letture, o rilanciare la sola parte percettiva quando arrivano
// nuove anteprime, non deve essere costretto a rifare anche l'altra.
//
// Il rebuild resta incluso: senza, i dHash appena calcolati non diventerebbero
// gruppi e il job non produrrebbe niente di visibile.
func (d *Dedup) RunPerceptual(jobID int) (string, error) {
	return d.perceptualAndGroups(jobID, tally{})
}

func (d *Dedup) perceptualAndGroups(jobID int, hashed tally) (string, error) {
	perceptual, err := d.hashThumbnails(jobID)
	if err != nil {
		return "", err
	}

	outcome, err := d.client.Rebuild()
	if err != nil {
		return "", fmt.Errorf("rebuild: %w", err)
	}

	return fmt.Sprintf(
		"%s, %s, %d gruppi esatti, %d gruppi simili",
		hashed.describe("sha256"), perceptual.describe("dHash"),
		outcome.GruppiEsatti, outcome.GruppiSimili), nil
}

// tally conta l'esito di una fase. I falliti finiscono nel risultato del job:
// restano in coda, e chi guarda la pagina Job deve poterlo sapere.
type tally struct {
	done, failed int
}

func (t tally) describe(what string) string {
	if t.failed == 0 {
		return fmt.Sprintf("%d %s", t.done, what)
	}
	return fmt.Sprintf("%d %s (%d non leggibili)", t.done, what, t.failed)
}

// hashFunc calcola l'hash di un elemento della coda. ok=false vuol dire che il
// file non si puo' leggere: l'elemento resta in coda e si ritenta al giro dopo.
type hashFunc func(item api.PendingMedia) (res api.HashResult, ok bool)

// drain svuota una coda di hash. La scorre per media_id invece di rileggerla
// dall'inizio: le code hash e dhash non hanno uno stato di errore, quindi un
// file illeggibile resta in testa per sempre. Rileggendo da capo, una pagina
// fatta solo di file illeggibili fermava la fase -- e il job si chiudeva 'done'
// come se niente fosse. Cosi' quei file si saltano e il resto avanza.
func (d *Dedup) drain(jobID int, stage string, fn hashFunc) (tally, error) {
	var t tally
	after := 0
	for {
		pending, err := d.client.GetPending(stage, d.cfg.Batch, after)
		if err != nil {
			return t, fmt.Errorf("coda %s: %w", stage, err)
		}
		if len(pending) == 0 {
			break
		}
		// Un'API che ignora "after" restituirebbe sempre la stessa pagina, e
		// questo ciclo non finirebbe mai.
		if pending[0].MediaID <= after {
			return t, fmt.Errorf("coda %s: l'API non scorre per media_id, va aggiornata", stage)
		}
		after = pending[len(pending)-1].MediaID

		results := make([]api.HashResult, 0, len(pending))
		for _, item := range pending {
			res, ok := fn(item)
			if !ok {
				t.failed++
				continue
			}
			results = append(results, res)
		}

		if len(results) > 0 {
			if err := d.client.SendHashes(results); err != nil {
				return t, fmt.Errorf("invio %s: %w", stage, err)
			}
			t.done += len(results)
		}
		// Un 409 dice che il job non e' piu' nostro: si smette subito, perche'
		// continuare significherebbe rifare il lavoro di un altro pod.
		if err := d.client.Heartbeat(jobID); err != nil {
			return t, err
		}
		d.log.Info("hash calcolati", "fase", stage, "totale", t.done, "falliti", t.failed)
	}
	if t.failed > 0 {
		d.log.Warn("file rimasti in coda", "fase", stage, "falliti", t.failed)
	}
	return t, nil
}

// hashOriginals legge i file veri: e' il passaggio costoso, perche' obbliga a
// una passata di lettura completa sulla share. E' anche il motivo per cui e'
// incrementale -- solo i media senza hash vengono letti, quindi dopo il primo
// giro costa quasi nulla.
func (d *Dedup) hashOriginals(jobID int) (tally, error) {
	return d.drain(jobID, "hash", func(item api.PendingMedia) (api.HashResult, bool) {
		sum, kind, err := hash.FileHash(d.originalPath(item))
		if err != nil {
			d.log.Warn("sha256 fallito", "media_id", item.MediaID, "file", item.FileName, "err", err)
			return api.HashResult{}, false
		}
		return api.HashResult{MediaID: item.MediaID, ContentHash: sum, HashKind: kind}, true
	})
}

// hashThumbnails lavora sulle thumbnail 's': ~15 KB invece di 6 MB, e sono
// gia' ridimensionate.
func (d *Dedup) hashThumbnails(jobID int) (tally, error) {
	return d.drain(jobID, "dhash", func(item api.PendingMedia) (api.HashResult, bool) {
		path := d.thumbPath(item.MediaID, "s")
		if _, err := os.Stat(path); err != nil {
			d.log.Debug("thumbnail assente", "media_id", item.MediaID)
			return api.HashResult{}, false
		}
		bits, err := hash.DHash(path)
		if err != nil {
			d.log.Warn("dHash fallito", "media_id", item.MediaID, "err", err)
			return api.HashResult{}, false
		}
		return api.HashResult{MediaID: item.MediaID, DHash: bits}, true
	})
}
